package crypto

import (
	"errors"
	"fmt"
)

// wrapKeyFor derives the symmetric key used to wrap (encrypt) the network key
// during the handshake. It depends only on the ephemeral ECDH secret and the
// server salt, so both peers can compute it without knowing the password.
func wrapKeyFor(shared, salt []byte) []byte {
	return expand(shared, salt, []byte("cslvpn/v1/keywrap"), KeyLen)
}

// WrapKey encrypts the network key (pwKey) for transport in the handshake. The
// network password therefore never appears on the wire, not even inside the
// encrypted handshake, which keeps it out of relay logs and memory dumps.
func WrapKey(wrapKey, pwKey []byte) ([]byte, error) {
	c, err := NewCipher(wrapKey)
	if err != nil {
		return nil, err
	}
	pt := append([]byte("cslvpn-key"), pwKey...)
	return c.Seal(0, 1, pt), nil
}

// UnwrapKey reverses WrapKey.
func UnwrapKey(wrapKey, sealed []byte) ([]byte, error) {
	c, err := NewCipher(wrapKey)
	if err != nil {
		return nil, err
	}
	typ, pt, err := c.Open(0, sealed)
	if err != nil || typ != 1 {
		return nil, ErrAuthFailed
	}
	if len(pt) != 10+KeyLen || string(pt[:10]) != "cslvpn-key" {
		return nil, errors.New("crypto: malformed key blob")
	}
	return pt[10:], nil
}

// WrapForHandshake is a convenience helper combining wrapKeyFor and WrapKey.
func WrapForHandshake(shared, salt, pwKey []byte) ([]byte, error) {
	return WrapKey(wrapKeyFor(shared, salt), pwKey)
}

// UnwrapForHandshake is a convenience helper combining wrapKeyFor and
// UnwrapKey.
func UnwrapForHandshake(shared, salt, sealed []byte) ([]byte, error) {
	if len(sealed) == 0 {
		return nil, fmt.Errorf("crypto: empty key blob")
	}
	return UnwrapKey(wrapKeyFor(shared, salt), sealed)
}
