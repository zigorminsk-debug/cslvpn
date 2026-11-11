package client

import (
	"bufio"
	"net"
	"os"
	"time"

	"github.com/zigorminsk-debug/cslvpn/internal/proto"
)

// link is a plaintext (pre-handshake) frame transport.
type link interface {
	read() (typ uint8, body []byte, err error)
	write(typ uint8, body []byte) error
	close() error
	setDeadline(t time.Time) error
	remote() string
}

// udpLink carries frames in single UDP datagrams.
type udpLink struct {
	c    *net.UDPConn
	buf  []byte
	name string
	// server is the resolved relay address; retained because the kernel forgets
	// the connected peer when a "Network is unreachable" error is returned, and
	// we must re-bind to the correct source address on the next attempt.
	server *net.UDPAddr
	port   int
}

func (l *udpLink) read() (uint8, []byte, error) {
	for {
		n, err := l.c.Read(l.buf)
		if err != nil {
			return 0, nil, err
		}
		typ, body, _, err := proto.ParseFrame(l.buf[:n])
		if err != nil {
			continue // garbage datagram
		}
		out := make([]byte, len(body))
		copy(out, body)
		return typ, out, nil
	}
}

func (l *udpLink) write(typ uint8, body []byte) error {
	_, err := l.c.Write(proto.AppendFrame(nil, typ, body))
	return err
}

// ensureLocalAddr re-binds the socket to the address the kernel would use for
// the relay. It is called after a "network is unreachable" error, which clears
// the socket's cached route.
func (l *udpLink) ensureLocalAddr() error {
	probe, err := net.DialUDP("udp", nil, l.server)
	if err != nil {
		return err
	}
	local := probe.LocalAddr().(*net.UDPAddr)
	_ = probe.Close()
	if la, ok := l.c.LocalAddr().(*net.UDPAddr); ok && la.String() != "0.0.0.0:0" && la.Port != 0 {
		if la.IP.Equal(local.IP) {
			return nil // already bound to a working source address
		}
	}
	bound, err := net.DialUDP("udp", &net.UDPAddr{IP: local.IP, Port: l.port}, l.server)
	if err != nil {
		return err
	}
	_ = bound.SetReadBuffer(4 << 20)
	_ = bound.SetWriteBuffer(4 << 20)
	old := l.c
	l.c = bound
	return old.Close()
}

func (l *udpLink) close() error                  { return l.c.Close() }
func (l *udpLink) setDeadline(t time.Time) error { return l.c.SetReadDeadline(t) }
func (l *udpLink) remote() string                { return l.name }

// tcpLink carries frames on a single stream with length prefixes.
type tcpLink struct {
	c  net.Conn
	fr *proto.FrameReader
}

func newTCPLink(c net.Conn) *tcpLink {
	return &tcpLink{c: c, fr: proto.NewFrameReader(bufio.NewReaderSize(c, 128*1024))}
}

func (l *tcpLink) read() (uint8, []byte, error) { return l.fr.Next() }

func (l *tcpLink) write(typ uint8, body []byte) error {
	return proto.WriteFrame(l.c, typ, body)
}

func (l *tcpLink) close() error                  { return l.c.Close() }
func (l *tcpLink) setDeadline(t time.Time) error { return l.c.SetDeadline(t) }
func (l *tcpLink) remote() string                { return l.c.RemoteAddr().String() }

// dial opens the transport the user selected. The relay listens for UDP and
// TCP on the same host:port, so switching transports needs no config change.
func dial(server string, tr proto.Transport) (link, error) {
	switch tr {
	case proto.TransportUDP:
		ra, err := net.ResolveUDPAddr("udp", server)
		if err != nil {
			return nil, err
		}
		c, err := net.DialUDP("udp", nil, ra)
		if err != nil {
			return nil, err
		}
		_ = c.SetReadBuffer(4 << 20)
		_ = c.SetWriteBuffer(4 << 20)
		return &udpLink{c: c, buf: make([]byte, 65535), name: ra.String(), server: ra, port: c.LocalAddr().(*net.UDPAddr).Port}, nil
	case proto.TransportTCP:
		c, err := net.DialTimeout("tcp", server, 15*time.Second)
		if err != nil {
			return nil, err
		}
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.SetNoDelay(true)
		}
		return newTCPLink(c), nil
	}
	return nil, proto.ErrShortBuffer
}

var osHostname = os.Hostname
