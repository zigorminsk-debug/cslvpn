package relay

import (
	"errors"
	"net"
	"sync"
	"time"

	"github.com/zigorminsk-debug/cslvpn/internal/proto"
)

// tcpListener serves the TCP transport. One TCP connection carries exactly one
// session, so no demultiplexing is needed and the kernel guarantees ordering
// and delivery - the right choice for restrictive firewalls that drop UDP.
type tcpListener struct {
	ln   net.Listener
	srv  *Server
	mu   sync.Mutex
	open map[net.Conn]struct{}
	wg   sync.WaitGroup
}

func listenTCP(addr string, srv *Server) (*tcpListener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &tcpListener{ln: ln, srv: srv, open: make(map[net.Conn]struct{})}, nil
}

func (l *tcpListener) addr() net.Addr { return l.ln.Addr() }

func (l *tcpListener) close() {
	_ = l.ln.Close()
	l.mu.Lock()
	for c := range l.open {
		_ = c.Close()
	}
	l.mu.Unlock()
}

func (l *tcpListener) serve() {
	for {
		conn, err := l.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetNoDelay(true)
			_ = tc.SetKeepAlive(true)
			_ = tc.SetKeepAlivePeriod(30 * time.Second)
		}
		l.mu.Lock()
		l.open[conn] = struct{}{}
		l.mu.Unlock()
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			s := &Session{
				srv:  l.srv,
				conn: &tcpConn{conn: conn},
				tr:   proto.TransportTCP,
				done: make(chan struct{}),
			}
			s.handle()
			l.mu.Lock()
			delete(l.open, conn)
			l.mu.Unlock()
			_ = conn.Close()
		}()
	}
}

// tcpConn adapts net.Conn to rawConn using the length-prefixed framing.
type tcpConn struct {
	conn net.Conn
	cr   *proto.FrameReader
}

func (c *tcpConn) reader() *proto.FrameReader {
	if c.cr == nil {
		c.cr = proto.NewFrameReader(c.conn)
	}
	return c.cr
}

func (c *tcpConn) Read() (uint8, []byte, error) { return c.reader().Next() }

func (c *tcpConn) Write(typ uint8, body []byte) error {
	return proto.WriteFrame(c.conn, typ, body)
}

func (c *tcpConn) Close() error                      { return c.conn.Close() }
func (c *tcpConn) RemoteAddr() net.Addr              { return c.conn.RemoteAddr() }
func (c *tcpConn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }
