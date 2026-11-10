package client

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/zigorminsk-debug/cslvpn/internal/crypto"
	"github.com/zigorminsk-debug/cslvpn/internal/proto"
)

// secure wraps a link with session keys: every frame from FrameWelcome on is
// AES-256-GCM sealed. The sequence number travels in the frame body so the
// relay can reject replays before doing any decryption work.
type secure struct {
	l       link
	sendC   *crypto.Cipher
	recvC   *crypto.Cipher
	sendSeq uint64
	recvSeq uint64
	seen    bool
}

func newSecure(l link, sendC, recvC *crypto.Cipher) *secure {
	return &secure{l: l, sendC: sendC, recvC: recvC}
}

func (s *secure) write(typ uint8, pt []byte) error {
	if !proto.IsEncrypted(typ) {
		return s.l.write(typ, pt)
	}
	seq := s.sendSeq
	s.sendSeq++
	sealed := s.sendC.Seal(seq, typ, pt)
	body := make([]byte, 4, 4+len(sealed)-1)
	binary.BigEndian.PutUint32(body, uint32(seq))
	body = append(body, sealed[1:]...)
	return s.l.write(typ, body)
}

func (s *secure) read() (uint8, []byte, error) {
	for {
		typ, body, err := s.l.read()
		if err != nil {
			return 0, nil, err
		}
		if !proto.IsEncrypted(typ) {
			return typ, body, nil
		}
		if len(body) < 4 {
			continue
		}
		seq := uint64(binary.BigEndian.Uint32(body[:4]))
		if s.seen && seq <= s.recvSeq {
			continue // replayed or reordered frame
		}
		s.recvSeq = seq
		s.seen = true
		t, pt, err := s.recvC.Open(seq, append([]byte{typ}, body[4:]...))
		if err != nil {
			return 0, nil, fmt.Errorf("frame authentication failed: %w", err)
		}
		return t, pt, nil
	}
}

func (s *secure) setDeadline(t time.Time) error { return s.l.setDeadline(t) }
func (s *secure) close() error                  { return s.l.close() }
func (s *secure) remote() string                { return s.l.remote() }
