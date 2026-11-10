package proto

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Frame wire layout (identical for TCP and UDP):
//
//	u16 length (big endian, = 1 + len(body))
//	u8  type
//	body[length-1]
//
// Control handshake frames carry plain text bodies; every frame from
// FrameWelcome on carries an AEAD sealed body (see internal/crypto).
const maxFrameBody = MaxFrameSize

// ErrFrameTooLarge is returned when a peer announces an oversized frame.
var ErrFrameTooLarge = errors.New("proto: frame too large")

// WriteFrame writes one framed message to w.
func WriteFrame(w io.Writer, typ uint8, body []byte) error {
	if len(body)+1 > maxFrameBody {
		return fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, len(body))
	}
	var hdr [3]byte
	binary.BigEndian.PutUint16(hdr[0:], uint16(len(body)+1))
	hdr[2] = typ
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	_, err := w.Write(body)
	return err
}

// AppendFrame appends one framed message to dst (for datagram transports).
func AppendFrame(dst []byte, typ uint8, body []byte) []byte {
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(body)+1))
	dst = append(dst, typ)
	return append(dst, body...)
}

// ParseFrame splits one framed message from b. It returns the type, the body
// and the number of consumed bytes.
func ParseFrame(b []byte) (typ uint8, body []byte, consumed int, err error) {
	if len(b) < 3 {
		return 0, nil, 0, ErrShortBuffer
	}
	n := int(binary.BigEndian.Uint16(b))
	if n < 1 || n > maxFrameBody {
		return 0, nil, 0, ErrFrameTooLarge
	}
	if len(b) < 2+n {
		return 0, nil, 0, ErrShortBuffer
	}
	return b[2], b[3 : 2+n], 2 + n, nil
}

// FrameReader reads framed messages from a stream (TCP).
type FrameReader struct{ r *bufio.Reader }

// NewFrameReader wraps r.
func NewFrameReader(r io.Reader) *FrameReader {
	if br, ok := r.(*bufio.Reader); ok {
		return &FrameReader{r: br}
	}
	return &FrameReader{r: bufio.NewReaderSize(r, 64*1024)}
}

// Next returns the next frame.
func (fr *FrameReader) Next() (uint8, []byte, error) {
	var hdr [3]byte
	if _, err := io.ReadFull(fr.r, hdr[:2]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:2]))
	if n < 1 || n > maxFrameBody {
		return 0, nil, ErrFrameTooLarge
	}
	rest := make([]byte, n)
	if _, err := io.ReadFull(fr.r, rest); err != nil {
		return 0, nil, err
	}
	return rest[0], rest[1:], nil
}

// Hello is the first message a client sends.
type Hello struct {
	Version     uint8
	Transport   Transport
	Flags       uint8
	ClientPub   [32]byte
	ClientNonce [16]byte
	Network     string
	Member      string
	DesiredIP   uint32
}

// Marshal encodes the hello message.
func (h *Hello) Marshal() []byte {
	w := NewWriter()
	w.U8(h.Version)
	w.U8(uint8(h.Transport))
	w.U8(h.Flags)
	w.Raw(h.ClientPub[:])
	w.Raw(h.ClientNonce[:])
	w.String(h.Network)
	w.String(h.Member)
	w.U32(h.DesiredIP)
	return w.Done()
}

// UnmarshalHello parses a hello message.
func UnmarshalHello(b []byte) (*Hello, error) {
	r := NewReader(b)
	h := &Hello{Version: r.U8(), Transport: Transport(r.U8()), Flags: r.U8()}
	copy(h.ClientPub[:], r.Raw(32))
	copy(h.ClientNonce[:], r.Raw(16))
	h.Network = r.String()
	h.Member = r.String()
	h.DesiredIP = r.U32()
	if err := r.Err(); err != nil {
		return nil, err
	}
	return h, nil
}

// Challenge is the server's answer to Hello.
type Challenge struct {
	Version   uint8
	ServerPub [32]byte
	Salt      [16]byte
}

// Marshal encodes the challenge.
func (c *Challenge) Marshal() []byte {
	w := NewWriter()
	w.U8(c.Version)
	w.Raw(c.ServerPub[:])
	w.Raw(c.Salt[:])
	return w.Done()
}

// UnmarshalChallenge parses a challenge.
func UnmarshalChallenge(b []byte) (*Challenge, error) {
	r := NewReader(b)
	c := &Challenge{Version: r.U8()}
	copy(c.ServerPub[:], r.Raw(32))
	copy(c.Salt[:], r.Raw(16))
	if err := r.Err(); err != nil {
		return nil, err
	}
	return c, nil
}

// Proof is a 32 byte HMAC-SHA256 authentication proof.
type Proof [32]byte

// Marshal encodes the proof.
func (p Proof) Marshal() []byte { return append([]byte(nil), p[:]...) }

// UnmarshalProof parses a proof.
func UnmarshalProof(b []byte) (Proof, error) {
	var p Proof
	if len(b) != 32 {
		return p, ErrShortBuffer
	}
	copy(p[:], b)
	return p, nil
}

// Welcome is the first encrypted frame sent by the server.
type Welcome struct {
	IP         uint32
	Prefix     uint8
	MTU        uint16
	NetworkID  uint32
	MaxMembers uint16
	Transport  Transport
	Gateway    bool
}

// Marshal encodes the welcome frame.
func (w *Welcome) Marshal() []byte {
	b := NewWriter()
	b.U32(w.IP)
	b.U8(w.Prefix)
	b.U16(w.MTU)
	b.U32(w.NetworkID)
	b.U16(w.MaxMembers)
	b.U8(uint8(w.Transport))
	var gw uint8
	if w.Gateway {
		gw = 1
	}
	b.U8(gw)
	return b.Done()
}

// UnmarshalWelcome parses a welcome frame.
func UnmarshalWelcome(b []byte) (*Welcome, error) {
	r := NewReader(b)
	w := &Welcome{IP: r.U32(), Prefix: r.U8(), MTU: r.U16(), NetworkID: r.U32(), MaxMembers: r.U16(), Transport: Transport(r.U8()), Gateway: r.U8()&1 == 1}
	if err := r.Err(); err != nil {
		return nil, err
	}
	return w, nil
}

// Peer is one member of a virtual network.
type Peer struct {
	IP        uint32
	Name      string
	Transport Transport
	Flags     uint8
}

// PeerList is pushed by the server whenever membership changes.
type PeerList struct{ Peers []Peer }

// Marshal encodes the peer list.
func (p *PeerList) Marshal() []byte {
	w := NewWriter()
	n := len(p.Peers)
	if n > 255 {
		n = 255
	}
	w.U8(uint8(n))
	for _, pr := range p.Peers[:n] {
		w.U32(pr.IP)
		w.String(pr.Name)
		w.U8(uint8(pr.Transport))
		w.U8(pr.Flags)
	}
	return w.Done()
}

// UnmarshalPeerList parses a peer list.
func UnmarshalPeerList(b []byte) (*PeerList, error) {
	r := NewReader(b)
	n := int(r.U8())
	pl := &PeerList{Peers: make([]Peer, 0, n)}
	for i := 0; i < n; i++ {
		pr := Peer{IP: r.U32(), Name: r.String(), Transport: Transport(r.U8()), Flags: r.U8()}
		pl.Peers = append(pl.Peers, pr)
	}
	if err := r.Err(); err != nil {
		return nil, err
	}
	return pl, nil
}

// Data frame body: u32 destination virtual IP + raw inner IP packet.
func EncodeData(dst uint32, pkt []byte) []byte {
	out := make([]byte, 4+len(pkt))
	binary.BigEndian.PutUint32(out, dst)
	copy(out[4:], pkt)
	return out
}

// DecodeData parses a data frame body.
func DecodeData(b []byte) (dst uint32, pkt []byte, err error) {
	if len(b) < 4 {
		return 0, nil, ErrShortBuffer
	}
	return binary.BigEndian.Uint32(b), b[4:], nil
}

// EncodePing builds a ping/pong body.
func EncodePing(ts uint64) []byte {
	return binary.BigEndian.AppendUint64(nil, ts)
}

// DecodePing parses a ping/pong body.
func DecodePing(b []byte) (uint64, error) {
	if len(b) < 8 {
		return 0, ErrShortBuffer
	}
	return binary.BigEndian.Uint64(b), nil
}
