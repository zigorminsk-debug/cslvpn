package relay

import (
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zigorminsk-debug/cslvpn/internal/proto"
)

// udpListener multiplexes a single UDP socket into per-peer sessions. UDP is
// the default transport: it keeps latency low and survives NAT rebinding,
// while the inner tunnels carry reliable protocols (TCP/RDP/SMB) that handle
// the occasional lost datagram themselves.
type udpListener struct {
	pc  *net.UDPConn
	srv *Server

	mu       sync.Mutex
	sessions map[string]*udpConn
	closed   bool
}

// datagramReady is the depth of the per-peer queue. Deep enough to absorb a
// burst of a few hundred packets while a peer goroutine is busy.
const datagramQueue = 512

func listenUDP(addr string, srv *Server) (*udpListener, error) {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	pc, err := net.ListenUDP("udp", ua)
	if err != nil {
		return nil, err
	}
	// 4 MiB socket buffers so a relay on a busy server does not drop bursts.
	_ = pc.SetReadBuffer(4 << 20)
	_ = pc.SetWriteBuffer(4 << 20)
	return &udpListener{pc: pc, srv: srv, sessions: make(map[string]*udpConn)}, nil
}

func (l *udpListener) addr() net.Addr { return l.pc.LocalAddr() }

func (l *udpListener) close() {
	l.mu.Lock()
	l.closed = true
	sess := make([]*udpConn, 0, len(l.sessions))
	for _, c := range l.sessions {
		sess = append(sess, c)
	}
	l.mu.Unlock()
	_ = l.pc.Close()
	for _, c := range sess {
		c.shutdown()
	}
}

func (l *udpListener) serve() {
	buf := make([]byte, 65535)
	for {
		n, raddr, err := l.pc.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		typ, body, _, err := proto.ParseFrame(buf[:n])
		if err != nil {
			continue
		}
		// Copy: buf is reused by the next ReadFromUDP.
		msg := make([]byte, len(body))
		copy(msg, body)

		key := raddr.String()
		l.mu.Lock()
		c := l.sessions[key]
		if c == nil {
			if !l.closed && typ == proto.FrameHello {
				c = l.newSessionLocked(key, raddr)
			} else {
				l.mu.Unlock()
				continue
			}
		}
		l.mu.Unlock()
		c.push(typ, msg)
	}
}

func (l *udpListener) newSessionLocked(key string, raddr *net.UDPAddr) *udpConn {
	c := &udpConn{
		l:    l,
		addr: raddr,
		key:  key,
		ch:   make(chan datagram, datagramQueue),
		done: make(chan struct{}),
	}
	l.sessions[key] = c
	s := &Session{
		srv:  l.srv,
		conn: c,
		tr:   proto.TransportUDP,
		done: make(chan struct{}),
	}
	c.sess = s
	go func() {
		s.handle()
		// Make sure the demux entry disappears even on abnormal exits.
		l.remove(key)
	}()
	return c
}

func (l *udpListener) remove(key string) {
	l.mu.Lock()
	if c, ok := l.sessions[key]; ok {
		delete(l.sessions, key)
		c.shutdown()
	}
	l.mu.Unlock()
}

// datagram is one received frame.
type datagram struct {
	typ  uint8
	body []byte
}

// udpConn adapts the demultiplexer to the rawConn interface.
type udpConn struct {
	l    *udpListener
	sess *Session
	addr *net.UDPAddr
	key  string

	ch        chan datagram
	done      chan struct{}
	closeOnce sync.Once
	closed    atomic.Bool
	dropped   atomic.Uint64
}

func (c *udpConn) push(typ uint8, body []byte) {
	select {
	case c.ch <- datagram{typ: typ, body: body}:
	case <-c.done:
	default:
		c.dropped.Add(1)
	}
}

func (c *udpConn) Read() (uint8, []byte, error) {
	select {
	case <-c.done:
		return 0, nil, io.EOF
	case d := <-c.ch:
		return d.typ, d.body, nil
	}
}

func (c *udpConn) Write(typ uint8, body []byte) error {
	if c.closed.Load() {
		return net.ErrClosed
	}
	buf := proto.AppendFrame(nil, typ, body)
	_, err := c.l.pc.WriteToUDP(buf, c.addr)
	return err
}

func (c *udpConn) Close() error {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		close(c.done)
	})
	return nil
}

func (c *udpConn) shutdown() { _ = c.Close() }

func (c *udpConn) RemoteAddr() net.Addr { return c.addr }

func (c *udpConn) SetReadDeadline(time.Time) error { return nil } // not used for UDP
