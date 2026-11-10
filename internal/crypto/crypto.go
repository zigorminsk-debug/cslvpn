// Package crypto contains the CSLVPN v1 key schedule and AEAD layer.
//
// Design notes
//   - Network membership is proven by knowing the network password. The
//     password is stretched with PBKDF2-SHA256 and mixed into the session keys,
//     therefore a wrong password can never produce a working channel.
//   - Sessions use an ephemeral X25519 exchange, so a recorded session cannot
//     be decrypted later even if the password leaks (forward secrecy).
//   - Every frame is protected with AES-256-GCM; the frame type, sequence
//     number and plaintext length are authenticated as additional data.
//   - The relay server is stateless with respect to keys: it derives per
//     session keys from the handshake and cannot distinguish more than the
//     protocol requires (it does decrypt, because it relays).
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	// KeyLen is the AES-256 key length.
	KeyLen = 32
	// SaltLen is the handshake salt length.
	SaltLen = 16
	// NonceLen is the public handshake nonce length.
	NonceLen = 16
	// PBKDF2Iterations slows down offline password guessing.
	PBKDF2Iterations = 200_000

	infoHandshake = "cslvpn/v1/handshake"
	infoC2S       = "cslvpn/v1/client-to-server"
	infoS2C       = "cslvpn/v1/server-to-client"
	infoProofC    = "cslvpn/v1/client-proof"
	infoProofS    = "cslvpn/v1/server-proof"
)

// ErrAuthFailed is returned when an AEAD tag does not verify.
var ErrAuthFailed = errors.New("crypto: authentication failed")

// RandomBytes returns n cryptographically secure random bytes.
func RandomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto: system random source failed: " + err.Error())
	}
	return b
}

// PasswordKey stretches a network password into a 32 byte key. The network
// name is bound into the salt so the same password on two networks yields
// different keys.
func PasswordKey(network, password string) []byte {
	salt := sha256.Sum256([]byte("cslvpn/v1/network-salt/" + network))
	k, err := pbkdf2.Key(sha256.New, password, salt[:], PBKDF2Iterations, KeyLen)
	if err != nil {
		panic("crypto: pbkdf2: " + err.Error())
	}
	return k
}

// GenerateKeyPair creates an ephemeral X25519 key pair.
func GenerateKeyPair() (*ecdh.PrivateKey, error) {
	return ecdh.X25519().GenerateKey(rand.Reader)
}

// PublicKeyBytes returns the 32 byte public key encoding.
func PublicKeyBytes(k *ecdh.PrivateKey) [32]byte {
	var out [32]byte
	copy(out[:], k.PublicKey().Bytes())
	return out
}

// SharedSecret computes the X25519 shared secret.
func SharedSecret(priv *ecdh.PrivateKey, peerPub [32]byte) ([]byte, error) {
	pub, err := ecdh.X25519().NewPublicKey(peerPub[:])
	if err != nil {
		return nil, fmt.Errorf("crypto: bad peer public key: %w", err)
	}
	return priv.ECDH(pub)
}

// Transcript hashes the public handshake material plus the shared secret. The
// result binds both handshake messages to the derived keys (channel binding).
func Transcript(hello, challenge, shared []byte) []byte {
	h := sha256.New()
	h.Write([]byte(infoHandshake))
	h.Write(hello)
	h.Write(challenge)
	h.Write(shared)
	return h.Sum(nil)
}

// expand runs a HKDF-SHA256 style expansion.
func expand(secret, salt, info []byte, n int) []byte {
	// HKDF-Extract
	prk := hmacSHA256(salt, secret)
	// HKDF-Expand
	var out, prev []byte
	for i := byte(1); len(out) < n; i++ {
		h := hmac.New(sha256.New, prk)
		h.Write(prev)
		h.Write(info)
		h.Write([]byte{i})
		prev = h.Sum(nil)
		out = append(out, prev...)
	}
	return out[:n]
}

func hmacSHA256(key, msg []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(msg)
	return m.Sum(nil)
}

// SessionKeys derives the directional AEAD keys for a session.
// Only a peer that knows the network password can compute pwKey, so key
// derivation doubles as password verification.
func SessionKeys(shared, pwKey, transcript []byte) (c2s, s2c []byte) {
	ikm := make([]byte, 0, len(shared)+len(pwKey))
	ikm = append(ikm, shared...)
	ikm = append(ikm, pwKey...)
	master := expand(ikm, transcript[:16], []byte(infoHandshake), 64)
	return master[:KeyLen], master[KeyLen:]
}

// ClientProof produces the client's authentication proof.
func ClientProof(pwKey, transcript, shared []byte) []byte {
	return hmacSHA256(pwKey, append(append([]byte(infoProofC), transcript...), shared...))
}

// ServerProof produces the server's authentication proof, binding the client
// proof so both directions are confirmed.
func ServerProof(pwKey, transcript, shared, clientProof []byte) []byte {
	msg := append(append(append([]byte(infoProofS), transcript...), shared...), clientProof...)
	return hmacSHA256(pwKey, msg)
}

// Equal compares two proofs in constant time.
func Equal(a, b []byte) bool { return subtle.ConstantTimeCompare(a, b) == 1 }

// Cipher seals and opens frames of one direction of a session.
type Cipher struct {
	aead cipher.AEAD
}

// NewCipher builds an AES-256-GCM Cipher from a 32 byte key.
func NewCipher(key []byte) (*Cipher, error) {
	if len(key) != KeyLen {
		return nil, fmt.Errorf("crypto: bad key length %d", len(key))
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// nonce builds the 12 byte GCM nonce from the frame sequence number.
func nonce(seq uint64) []byte {
	var n [12]byte
	binary.BigEndian.PutUint64(n[4:], seq)
	return n[:]
}

// additional builds the authenticated additional data: frame type, sequence
// and plaintext length. Binding the type prevents frame type confusion, the
// sequence prevents reordering attacks below the replay window.
func additional(typ uint8, seq uint64, ptLen int) []byte {
	var aad [11]byte
	aad[0] = typ
	binary.BigEndian.PutUint64(aad[1:], seq)
	binary.BigEndian.PutUint16(aad[9:], uint16(ptLen))
	return aad[:]
}

// Seal encrypts pt as frame type typ with sequence number seq. The returned
// slice is a fresh buffer: type byte followed by ciphertext.
func (c *Cipher) Seal(seq uint64, typ uint8, pt []byte) []byte {
	out := make([]byte, 0, 1+len(pt)+c.aead.Overhead())
	out = append(out, typ)
	return c.aead.Seal(out, nonce(seq), pt, additional(typ, seq, len(pt)))
}

// Open verifies and decrypts a sealed frame (type byte + ciphertext).
func (c *Cipher) Open(seq uint64, sealed []byte) (uint8, []byte, error) {
	if len(sealed) < 1+c.aead.Overhead() {
		return 0, nil, ErrAuthFailed
	}
	typ := sealed[0]
	ct := sealed[1:]
	pt, err := c.aead.Open(nil, nonce(seq), ct, additional(typ, seq, len(ct)-c.aead.Overhead()))
	if err != nil {
		return 0, nil, ErrAuthFailed
	}
	return typ, pt, nil
}

// Overhead is the per frame expansion caused by the AEAD tag.
func (c *Cipher) Overhead() int { return c.aead.Overhead() }

// ReplayWindow implements a 64 packet sliding replay window (RFC 6479 style
// with a simple bitmap). It is used for the unreliable UDP transport; the
// ordered TCP transport only needs a monotonic counter.
type ReplayWindow struct {
	top    uint64 // highest accepted sequence + 1
	bitmap uint64
	seen   bool
}

// Check marks seq as seen and reports whether it is fresh. Frames that are too
// old (>64 behind) or duplicated are rejected.
func (w *ReplayWindow) Check(seq uint64) bool {
	if !w.seen {
		w.seen = true
		w.top = seq + 1
		w.bitmap = 1
		return true
	}
	if seq >= w.top {
		shift := seq + 1 - w.top
		if shift >= 64 {
			w.bitmap = 1
		} else {
			w.bitmap = (w.bitmap << shift) | 1
		}
		w.top = seq + 1
		return true
	}
	diff := w.top - 1 - seq
	if diff >= 64 {
		return false
	}
	bit := uint64(1) << diff
	if w.bitmap&bit != 0 {
		return false
	}
	w.bitmap |= bit
	return true
}
