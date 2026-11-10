package crypto

import (
	"bytes"
	"testing"
)

func TestPasswordKeyDeterministic(t *testing.T) {
	a := PasswordKey("office", "s3cret")
	b := PasswordKey("office", "s3cret")
	if !bytes.Equal(a, b) {
		t.Fatal("same network+password must give the same key")
	}
	if bytes.Equal(a, PasswordKey("office2", "s3cret")) {
		t.Fatal("the network name must be bound into the key")
	}
	if bytes.Equal(a, PasswordKey("office", "other")) {
		t.Fatal("different passwords must give different keys")
	}
	if len(a) != KeyLen {
		t.Fatalf("key length %d", len(a))
	}
}

func TestHandshakeDerivesMatchingKeys(t *testing.T) {
	cliPriv, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	srvPriv, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	cliPub := PublicKeyBytes(cliPriv)
	srvPub := PublicKeyBytes(srvPriv)

	cliShared, err := SharedSecret(cliPriv, srvPub)
	if err != nil {
		t.Fatal(err)
	}
	srvShared, err := SharedSecret(srvPriv, cliPub)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(cliShared, srvShared) {
		t.Fatal("ECDH shared secrets differ")
	}

	hello := []byte("hello-bytes")
	challenge := []byte("challenge-bytes")
	tr := Transcript(hello, challenge, cliShared)
	pw := PasswordKey("net", "pw")

	c2sC, s2cC := SessionKeys(cliShared, pw, tr)
	c2sS, s2cS := SessionKeys(srvShared, pw, tr)
	if !bytes.Equal(c2sC, c2sS) || !bytes.Equal(s2cC, s2cS) {
		t.Fatal("server and client derived different session keys")
	}
	if bytes.Equal(c2sC, s2cC) {
		t.Fatal("the two directions must use different keys")
	}

	// A man in the middle with a different password derives different keys.
	wrong := PasswordKey("net", "wrong")
	c2sW, _ := SessionKeys(cliShared, wrong, tr)
	if bytes.Equal(c2sW, c2sC) {
		t.Fatal("wrong password produced the same key")
	}
}

func TestProofs(t *testing.T) {
	pw := PasswordKey("net", "pw")
	shared := RandomBytes(32)
	tr := Transcript([]byte("h"), []byte("c"), shared)

	cp := ClientProof(pw, tr, shared)
	if len(cp) != 32 {
		t.Fatalf("proof length %d", len(cp))
	}
	if Equal(cp, ClientProof(PasswordKey("net", "x"), tr, shared)) {
		t.Fatal("proof must depend on the password")
	}
	sp := ServerProof(pw, tr, shared, cp)
	if Equal(sp, cp) {
		t.Fatal("client and server proofs must differ")
	}
	if !Equal(sp, ServerProof(pw, tr, shared, cp)) {
		t.Fatal("server proof is not deterministic")
	}
}

func TestSealOpenAndTamper(t *testing.T) {
	key := RandomBytes(KeyLen)
	c, err := NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	pt := []byte("inner ip packet")

	sealed := c.Seal(7, 8, pt)
	typ, got, err := c.Open(7, sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if typ != 8 || !bytes.Equal(got, pt) {
		t.Fatalf("round trip mismatch: type=%d body=%q", typ, got)
	}

	// Wrong sequence number (replay with a different counter).
	if _, _, err := c.Open(8, sealed); err == nil {
		t.Fatal("frame must not open with a different sequence number")
	}
	// Flip one byte.
	bad := append([]byte(nil), sealed...)
	bad[len(bad)-1] ^= 0x01
	if _, _, err := c.Open(7, bad); err == nil {
		t.Fatal("tampered frame opened")
	}
	// Truncated.
	if _, _, err := c.Open(7, sealed[:3]); err == nil {
		t.Fatal("truncated frame opened")
	}
}

func TestKeyWrapRoundTrip(t *testing.T) {
	shared := RandomBytes(32)
	salt := RandomBytes(SaltLen)
	pw := PasswordKey("net", "pw")

	blob, err := WrapForHandshake(shared, salt, pw)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, pw) {
		t.Fatal("the wrapped blob must not contain the raw key")
	}
	got, err := UnwrapForHandshake(shared, salt, blob)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pw) {
		t.Fatal("unwrapped key differs")
	}
	if _, err := UnwrapForHandshake(RandomBytes(32), salt, blob); err == nil {
		t.Fatal("a different shared secret must not unwrap the key")
	}
}

func TestReplayWindow(t *testing.T) {
	var w ReplayWindow
	for seq := uint64(0); seq < 10; seq++ {
		if !w.Check(seq) {
			t.Fatalf("fresh sequence %d rejected", seq)
		}
	}
	if w.Check(5) {
		t.Fatal("duplicate accepted")
	}
	if !w.Check(20) {
		t.Fatal("jump forward rejected")
	}
	if !w.Check(18) {
		t.Fatal("reordered packet inside the window rejected")
	}
	if w.Check(18) {
		t.Fatal("duplicate after reorder accepted")
	}
	if w.Check(3) {
		t.Fatal("packet outside the window accepted")
	}
}
