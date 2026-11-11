// Package proto implements the CSLVPN v1 wire format: a tiny length-prefixed
// framing layer, a dependency-free binary encoder/decoder and the control
// messages exchanged between client and relay server.
//
// Everything here is deliberately dependency-free (stdlib only) so that the
// project builds reproducibly on any platform without network access to module
// proxies.
package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Version is the wire protocol version implemented by this package.
const Version = 1

// Frame types. Types < FrameWelcome are exchanged in clear text during the
// handshake, everything from FrameWelcome on is always AEAD-encrypted.
const (
	FrameHello     uint8 = 1 // client -> server, plain
	FrameChallenge uint8 = 2 // server -> client, plain
	FrameAuthProof uint8 = 3 // client -> server, plain
	FrameAuthOK    uint8 = 4 // server -> client, plain
	FrameAuthErr   uint8 = 5 // server -> client, plain
	FrameWelcome   uint8 = 6 // server -> client, encrypted
	FramePeerList  uint8 = 7 // server -> client, encrypted
	FrameData      uint8 = 8 // both ways, encrypted
	FramePing      uint8 = 9 // both ways, encrypted
	FramePong      uint8 = 10
	FrameBye       uint8 = 11 // both ways, encrypted
	FrameKicked    uint8 = 12 // server -> client, encrypted
)

// IsEncrypted reports whether frames of this type must be protected by the
// session AEAD keys.
func IsEncrypted(t uint8) bool { return t >= FrameWelcome }

// MaxFrameSize bounds the size of a single frame body (without the 2-byte
// length prefix). A /24 virtual network with a 1500 byte MTU never needs more
// than ~1.5 KiB; 64 KiB leaves headroom for future control messages.
const MaxFrameSize = 64 * 1024

// ErrShortBuffer is returned when a buffer does not contain a complete message.
var ErrShortBuffer = errors.New("proto: short buffer")

// Writer is a minimal big-endian binary encoder.
type Writer struct{ b []byte }

// NewWriter returns an empty Writer.
func NewWriter() *Writer { return &Writer{} }

func (w *Writer) U8(v uint8)   { w.b = append(w.b, v) }
func (w *Writer) U16(v uint16) { w.b = binary.BigEndian.AppendUint16(w.b, v) }
func (w *Writer) U32(v uint32) { w.b = binary.BigEndian.AppendUint32(w.b, v) }
func (w *Writer) U64(v uint64) { w.b = binary.BigEndian.AppendUint64(w.b, v) }

// Raw appends bytes without a length prefix.
func (w *Writer) Raw(p []byte) { w.b = append(w.b, p...) }

// Bytes appends a u16-length-prefixed byte slice.
func (w *Writer) Bytes(p []byte) {
	if len(p) > 0xffff {
		p = p[:0xffff]
	}
	w.U16(uint16(len(p)))
	w.b = append(w.b, p...)
}

// String appends a u8-length-prefixed UTF-8 string (max 255 bytes).
func (w *Writer) String(s string) {
	if len(s) > 255 {
		s = s[:255]
	}
	w.U8(uint8(len(s)))
	w.b = append(w.b, s...)
}

// Done returns the encoded buffer.
func (w *Writer) Done() []byte { return w.b }

// Reader is a minimal big-endian binary decoder over a byte slice.
type Reader struct {
	b   []byte
	err error
}

// NewReader returns a Reader over b.
func NewReader(b []byte) *Reader { return &Reader{b: b} }

func (r *Reader) fail() {
	if r.err == nil {
		r.err = ErrShortBuffer
	}
}

// Err returns the first decoding error, if any.
func (r *Reader) Err() error { return r.err }

// Remaining returns the number of unread bytes.
func (r *Reader) Remaining() int { return len(r.b) }

func (r *Reader) U8() uint8 {
	if r.err != nil || len(r.b) < 1 {
		r.fail()
		return 0
	}
	v := r.b[0]
	r.b = r.b[1:]
	return v
}

func (r *Reader) U16() uint16 {
	if r.err != nil || len(r.b) < 2 {
		r.fail()
		return 0
	}
	v := binary.BigEndian.Uint16(r.b)
	r.b = r.b[2:]
	return v
}

func (r *Reader) U32() uint32 {
	if r.err != nil || len(r.b) < 4 {
		r.fail()
		return 0
	}
	v := binary.BigEndian.Uint32(r.b)
	r.b = r.b[4:]
	return v
}

func (r *Reader) U64() uint64 {
	if r.err != nil || len(r.b) < 8 {
		r.fail()
		return 0
	}
	v := binary.BigEndian.Uint64(r.b)
	r.b = r.b[8:]
	return v
}

// Raw reads n bytes.
func (r *Reader) Raw(n int) []byte {
	if r.err != nil || n < 0 || len(r.b) < n {
		r.fail()
		return nil
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}

// Bytes reads a u16-length-prefixed slice.
func (r *Reader) Bytes() []byte { return r.Raw(int(r.U16())) }

// String reads a u8-length-prefixed string.
func (r *Reader) String() string { return string(r.Raw(int(r.U8()))) }

// EncodeError builds a marshalled FrameAuthErr frame.
func EncodeError(code uint8, msg string) []byte {
	w := NewWriter()
	w.U8(code)
	w.String(msg)
	return w.Done()
}

// DecodeError parses a FrameAuthErr body.
func DecodeError(b []byte) (code uint8, msg string) {
	r := NewReader(b)
	code = r.U8()
	msg = r.String()
	if err := r.Err(); err != nil {
		return 1, "malformed error frame"
	}
	return code, msg
}

// Error codes carried by FrameAuthErr.
const (
	ErrCodeGeneric        uint8 = 1
	ErrCodeBadVersion     uint8 = 2
	ErrCodeNoSuchNetwork  uint8 = 3
	ErrCodeBadPassword    uint8 = 4
	ErrCodeNetworkFull    uint8 = 5
	ErrCodeBanned         uint8 = 6
	ErrCodeNoFreeAddr     uint8 = 7
	ErrCodeServerDisabled uint8 = 8
	ErrCodeNetworkBusy    uint8 = 9
	ErrCodeNameTaken      uint8 = 10
	ErrCodeBadRequest     uint8 = 11
)

// ErrorCodeString returns a human readable name for an error code.
func ErrorCodeString(c uint8) string {
	switch c {
	case ErrCodeGeneric:
		return "error"
	case ErrCodeBadVersion:
		return "unsupported protocol version"
	case ErrCodeNoSuchNetwork:
		return "network not found (wrong name or network creation disabled)"
	case ErrCodeBadPassword:
		return "wrong network password"
	case ErrCodeNetworkFull:
		return "network is full"
	case ErrCodeBanned:
		return "banned from this network"
	case ErrCodeNoFreeAddr:
		return "no free virtual address in network subnet"
	case ErrCodeServerDisabled:
		return "network is disabled by administrator"
	case ErrCodeNetworkBusy:
		return "network is temporarily unavailable"
	case ErrCodeNameTaken:
		return "member name already online in this network"
	case ErrCodeBadRequest:
		return "malformed request"
	}
	return fmt.Sprintf("error code %d", c)
}

// Transport identifies the data-plane transport selected by the client.
type Transport uint8

const (
	// TransportUDP carries the tunnel over UDP datagrams (low latency, best
	// effort delivery; reliable streams inside the tunnel handle loss).
	TransportUDP Transport = 1
	// TransportTCP carries the tunnel over a single TCP stream (guaranteed
	// delivery, robust against UDP blocking, head-of-line blocking).
	TransportTCP Transport = 2
)

// String returns a stable lower-case name of the transport.
func (t Transport) String() string {
	switch t {
	case TransportUDP:
		return "udp"
	case TransportTCP:
		return "tcp"
	}
	return "unknown"
}

// ParseTransport maps a CLI string to a Transport.
func ParseTransport(s string) (Transport, error) {
	switch s {
	case "udp", "UDP", "":
		return TransportUDP, nil
	case "tcp", "TCP":
		return TransportTCP, nil
	}
	return 0, fmt.Errorf("proto: unknown transport %q (want udp or tcp)", s)
}

// Hello flags.
const (
	// HelloFlagCreate asks the server to create the network if it does not
	// exist yet (Radmin-like "create network with name + password").
	HelloFlagCreate uint8 = 1 << 0
	// HelloFlagNoRetry disables server side member replacement.
	HelloFlagNoRetry uint8 = 1 << 1
	// HelloFlagInternet asks the relay to route the client's traffic to the
	// internet (the relay applies NAT), like a classic VPN gateway.
	HelloFlagInternet uint8 = 1 << 2
)

// Peer flags used in FramePeerList.
const (
	PeerFlagSelf      uint8 = 1 << 0
	PeerFlagInitiator uint8 = 1 << 1 // reserved for future P2P upgrades
)
