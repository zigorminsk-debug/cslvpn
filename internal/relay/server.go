// Package relay implements the CSLVPN relay server. The relay never carries
// traffic of its own: it authenticates clients, gives them a virtual address
// inside an isolated network ("room") and forwards encrypted frames between
// the members of that room. Clients in different rooms can never reach each
// other, exactly like Radmin VPN networks.
package relay

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zigorminsk-debug/cslvpn/internal/crypto"
	"github.com/zigorminsk-debug/cslvpn/internal/proto"
	"github.com/zigorminsk-debug/cslvpn/internal/tun"
)

// Stats holds relay wide counters exposed by the admin API.
type Stats struct {
	Sessions   atomic.Int64
	RxFrames   atomic.Uint64
	TxFrames   atomic.Uint64
	RxBytes    atomic.Uint64
	TxBytes    atomic.Uint64
	Dropped    atomic.Uint64
	Handshakes atomic.Uint64
}

// Server is one relay endpoint (UDP, TCP or both).
type Server struct {
	cfg Config
	reg *Registry
	log *log.Logger
	sts Stats

	udp *udpListener
	tcp *tcpListener
	gw  *gateway
}

// NewServer creates a relay server with the given limits.
func NewServer(cfg Config, logger *log.Logger) *Server {
	def := DefaultConfig()
	if cfg.Keepalive == 0 {
		cfg.Keepalive = def.Keepalive
	}
	if cfg.PeerTimeout == 0 {
		cfg.PeerTimeout = def.PeerTimeout
	}
	if cfg.Logf == nil {
		cfg.Logf = logger.Printf
	}
	s := &Server{cfg: cfg, log: logger}
	s.reg = NewRegistry(cfg)
	return s
}

// Registry exposes the network registry (used by the admin API).
func (s *Server) Registry() *Registry { return s.reg }

// StatsSnapshot returns the current counters.
func (s *Server) StatsSnapshot() map[string]uint64 {
	return map[string]uint64{
		"sessions":   uint64(s.sts.Sessions.Load()),
		"networks":   uint64(len(s.reg.Networks())),
		"rx_frames":  s.sts.RxFrames.Load(),
		"tx_frames":  s.sts.TxFrames.Load(),
		"rx_bytes":   s.sts.RxBytes.Load(),
		"tx_bytes":   s.sts.TxBytes.Load(),
		"dropped":    s.sts.Dropped.Load(),
		"handshakes": s.sts.Handshakes.Load(),
	}
}

// StartUDP binds a UDP socket and starts serving.
func (s *Server) StartUDP(addr string) error {
	l, err := listenUDP(addr, s)
	if err != nil {
		return err
	}
	s.udp = l
	go l.serve()
	return nil
}

// StartTCP binds a TCP listener and starts serving.
func (s *Server) StartTCP(addr string) error {
	l, err := listenTCP(addr, s)
	if err != nil {
		return err
	}
	s.tcp = l
	go l.serve()
	return nil
}

// UDPAddr returns the bound UDP address (after StartUDP).
func (s *Server) UDPAddr() net.Addr {
	if s.udp == nil {
		return nil
	}
	return s.udp.addr()
}

// TCPAddr returns the bound TCP address (after StartTCP).
func (s *Server) TCPAddr() net.Addr {
	if s.tcp == nil {
		return nil
	}
	return s.tcp.addr()
}

// tunDevice is the part of the TUN device the relay gateway needs.
type tunDevice interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
	Name() string
}

// EnableGateway routes internet-bound traffic of clients through a TUN device
// on the relay and applies NAT for the virtual subnet. iface must be an
// existing, configured TUN interface (see docs/SERVER.md for setup).
func (s *Server) EnableGateway(d tunDevice, cidr string) error {
	if err := tun.AddNAT(d.Name(), cidr); err != nil {
		return err
	}
	// Return traffic from the internet is addressed to the virtual addresses, so
	// the kernel needs a route for the whole virtual space through the gateway.
	if err := tun.RouteVia(cidr, d.Name()); err != nil {
		s.cfg.Logf("warning: could not add the virtual subnet route: %v", err)
	}
	s.gw = &gateway{dev: d, srv: s}
	go s.gw.run()
	s.cfg.Logf("gateway enabled on %s: clients requesting internet access are NATed to %s", d.Name(), cidr)
	return nil
}

// Shutdown stops the listeners.
func (s *Server) Shutdown() {
	if s.udp != nil {
		s.udp.close()
	}
	if s.tcp != nil {
		s.tcp.close()
	}
	if s.gw != nil {
		_ = s.gw.dev.Close()
	}
}

// gateway delivers return traffic coming from the internet back to the client
// that owns the destination virtual address.
type gateway struct {
	dev tunDevice
	srv *Server
}

func (g *gateway) write(pkt []byte) error { _, err := g.dev.Write(pkt); return err }

func (g *gateway) run() {
	buf := make([]byte, 65535)
	for {
		n, err := g.dev.Read(buf)
		if err != nil {
			return
		}
		if n < 20 {
			continue
		}
		dst := binary.BigEndian.Uint32(buf[16:20])
		if g.srv.cfg.Debug {
			g.srv.cfg.Logf("data: internet -> %s (%d bytes) from gateway", ipString(dst), n)
		}
		sess := g.srv.reg.SessionByIP(dst)
		if sess == nil {
			g.srv.sts.Dropped.Add(1)
			continue
		}
		body := make([]byte, 4+n)
		binary.BigEndian.PutUint32(body, dst)
		copy(body[4:], buf[:n])
		if err := sess.send(proto.FrameData, body); err != nil {
			g.srv.sts.Dropped.Add(1)
			continue
		}
		sess.txBytes.Add(uint64(n))
		g.srv.sts.TxFrames.Add(1)
	}
}

// rawConn abstracts the framing of a transport. Bodies handed to Write and
// returned by Read are unframed messages (type + body); the transport adds
// framing and, for UDP, datagram boundaries.
type rawConn interface {
	Read() (typ uint8, body []byte, err error)
	Write(typ uint8, body []byte) error
	Close() error
	RemoteAddr() net.Addr
	SetReadDeadline(t time.Time) error
}

// Session is one authenticated client connection.
type Session struct {
	srv  *Server
	conn rawConn
	tr   proto.Transport

	sendMu  sync.Mutex
	sendSeq uint64
	sendC   *crypto.Cipher

	recvSeq    uint64
	recvC      *crypto.Cipher
	recvWindow crypto.ReplayWindow
	recvFirst  bool

	member   *Member
	network  *Network
	internet bool // client asked for internet access through the relay
	dev      tunDevice

	closeOnce sync.Once
	done      chan struct{}

	lastSeen     atomic.Int64 // unix nanos
	listMu       sync.Mutex
	lastPeerList []byte
	rxBytes      atomic.Uint64
	txBytes      atomic.Uint64
	started      time.Time
}

// Transport returns the transport of the session.
func (s *Session) Transport() proto.Transport { return s.tr }

// Close tears the session down (idempotent).
func (s *Session) Close(reason string) { s.close(reason) }

func (s *Session) close(reason string) {
	s.closeOnce.Do(func() {
		close(s.done)
		_ = s.conn.Close()
		if s.member != nil && s.network != nil {
			s.srv.reg.Detach(s)
			s.broadcastPeers()
		}
		s.srv.sts.Sessions.Add(-1)
		s.srv.cfg.Logf("session %s (%s) closed: %s", s.remoteString(), s.tr, reason)
	})
}

func (s *Session) remoteString() string {
	if s.conn == nil {
		return "?"
	}
	if a := s.conn.RemoteAddr(); a != nil {
		return a.String()
	}
	return "?"
}

// ---- send path -------------------------------------------------------------

// send seals (when the frame type is encrypted) and writes one frame.
func (s *Session) send(typ uint8, pt []byte) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	var body []byte
	if proto.IsEncrypted(typ) {
		seq := s.sendSeq
		s.sendSeq++
		sealed := s.sendC.Seal(seq, typ, pt)
		body = make([]byte, 4, 4+len(sealed)-1)
		binary.BigEndian.PutUint32(body, uint32(seq))
		body = append(body, sealed[1:]...)
	} else {
		body = pt
	}
	if err := s.conn.Write(typ, body); err != nil {
		s.srv.cfg.Logf("session %s: write error: %v", s.remoteString(), err)
		return err
	}
	s.srv.sts.TxFrames.Add(1)
	return nil
}

// ---- receive path ----------------------------------------------------------

// receive reads and decrypts the next frame. Plain handshake frames are
// returned as is.
func (s *Session) receive() (uint8, []byte, error) {
	for {
		typ, body, err := s.conn.Read()
		if err != nil {
			return 0, nil, err
		}
		s.lastSeen.Store(time.Now().UnixNano())
		s.srv.sts.RxFrames.Add(1)
		if !proto.IsEncrypted(typ) {
			s.srv.sts.RxBytes.Add(uint64(len(body)))
			return typ, body, nil
		}
		if len(body) < 4 {
			s.srv.sts.Dropped.Add(1)
			continue
		}
		seq := uint64(binary.BigEndian.Uint32(body[:4]))
		if s.tr == proto.TransportUDP {
			if !s.recvWindow.Check(seq) {
				s.srv.sts.Dropped.Add(1)
				continue // duplicate or too old
			}
		} else {
			if s.recvFirst && seq <= s.recvSeq {
				s.srv.sts.Dropped.Add(1)
				continue
			}
		}
		s.recvSeq = seq
		s.recvFirst = true
		t, pt, err := s.recvC.Open(seq, append([]byte{typ}, body[4:]...))
		if err != nil {
			s.srv.sts.Dropped.Add(1)
			continue // unauthentic frame, drop silently
		}
		if t != typ {
			s.srv.sts.Dropped.Add(1)
			continue
		}
		return typ, pt, nil
	}
}

// ---- handshake -------------------------------------------------------------

// handle runs the handshake and then the relay loop for the session.
func (s *Session) handle() {
	defer func() {
		if r := recover(); r != nil {
			s.srv.cfg.Logf("session %s: panic: %v", s.remoteString(), r)
			s.close("internal error")
		}
	}()

	if err := s.handshake(); err != nil {
		s.srv.cfg.Logf("session %s: handshake failed: %v", s.remoteString(), err)
		s.close("handshake failed")
		return
	}
	s.srv.sts.Handshakes.Add(1)
	s.loop()
}

func (s *Session) handshake() error {
	if s.tr == proto.TransportTCP {
		_ = s.conn.SetReadDeadline(time.Now().Add(15 * time.Second))
		defer func() { _ = s.conn.SetReadDeadline(time.Time{}) }()
	}

	// ---- step 1: Hello -----------------------------------------------------
	var hello *proto.Hello
	var helloBytes []byte
	for {
		typ, body, err := s.conn.Read()
		if err != nil {
			return fmt.Errorf("waiting for hello: %w", err)
		}
		if typ != proto.FrameHello {
			continue
		}
		h, err := proto.UnmarshalHello(body)
		if err != nil {
			return fmt.Errorf("bad hello: %w", err)
		}
		hello, helloBytes = h, body
		break
	}
	if hello.Version != proto.Version {
		_ = s.send(proto.FrameAuthErr, proto.EncodeError(proto.ErrCodeBadVersion, ""))
		return fmt.Errorf("client protocol version %d != %d", hello.Version, proto.Version)
	}
	if hello.Flags&proto.HelloFlagCreate != 0 && !s.srv.cfg.AllowCreate {
		_ = s.send(proto.FrameAuthErr, proto.EncodeError(proto.ErrCodeNoSuchNetwork, ""))
		return errors.New("network creation disabled")
	}

	// ---- step 2: Challenge -------------------------------------------------
	priv, err := crypto.GenerateKeyPair()
	if err != nil {
		return err
	}
	srvPub := crypto.PublicKeyBytes(priv)
	ch := &proto.Challenge{Version: proto.Version, ServerPub: srvPub}
	salt := crypto.RandomBytes(crypto.SaltLen)
	copy(ch.Salt[:], salt)
	chBytes := ch.Marshal()
	if err := s.send(proto.FrameChallenge, chBytes); err != nil {
		return err
	}

	// ---- step 3: proof + wrapped network key -------------------------------
	var proofBytes, wrapped []byte
	for {
		typ, body, err := s.conn.Read()
		if err != nil {
			return fmt.Errorf("waiting for proof: %w", err)
		}
		switch typ {
		case proto.FrameAuthProof:
			proofBytes, wrapped, err = splitProof(body)
			if err != nil {
				_ = s.send(proto.FrameAuthErr, proto.EncodeError(proto.ErrCodeBadRequest, "malformed proof"))
				return err
			}
		case proto.FrameHello:
			// Retransmission of hello: keep waiting, the client will follow
			// with the proof for the challenge it already received.
			continue
		default:
			continue
		}
		break
	}

	shared, err := crypto.SharedSecret(priv, hello.ClientPub)
	if err != nil {
		return err
	}
	transcript := crypto.Transcript(helloBytes, chBytes, shared)

	// Unwrap the network key: it is encrypted under a key derived from the
	// ephemeral ECDH secret, so only the two parties can read it and a network
	// password never travels in plain text.
	pwKey, err := crypto.UnwrapForHandshake(shared, salt, wrapped)
	if err != nil {
		_ = s.send(proto.FrameAuthErr, proto.EncodeError(proto.ErrCodeBadRequest, "cannot unwrap network key"))
		return err
	}

	expect := crypto.ClientProof(pwKey, transcript, shared)
	if !crypto.Equal(expect, proofBytes) {
		_ = s.send(proto.FrameAuthErr, proto.EncodeError(proto.ErrCodeBadPassword, ""))
		return errors.New("bad network password")
	}

	// Key schedule (identical on the client).
	c2sKey, s2cKey := crypto.SessionKeys(shared, pwKey, transcript)
	s.recvC, err = crypto.NewCipher(c2sKey)
	if err != nil {
		return err
	}
	s.sendC, err = crypto.NewCipher(s2cKey)
	if err != nil {
		return err
	}

	// ---- step 4: register in network ---------------------------------------
	netw, ip, err := s.srv.reg.Attach(hello, pwKey, s)
	if err != nil {
		code := proto.ErrCodeGeneric
		switch {
		case errors.Is(err, ErrBadPassword):
			code = proto.ErrCodeBadPassword
		case errors.Is(err, ErrNotFound):
			code = proto.ErrCodeNoSuchNetwork
		case errors.Is(err, ErrNetworkFull):
			code = proto.ErrCodeNetworkFull
		case errors.Is(err, ErrNoFreeAddr):
			code = proto.ErrCodeNoFreeAddr
		case errors.Is(err, ErrBanned):
			code = proto.ErrCodeBanned
		case errors.Is(err, ErrNameTaken):
			code = proto.ErrCodeNameTaken
		case errors.Is(err, ErrLimit):
			code = proto.ErrCodeNetworkBusy
		}
		_ = s.send(proto.FrameAuthErr, proto.EncodeError(code, err.Error()))
		return err
	}

	srvProof := crypto.ServerProof(pwKey, transcript, shared, proofBytes)
	ok := proto.NewWriter()
	ok.Raw(srvProof)

	// The transport is decided by the socket the client reached us on, and the
	// client's Hello must agree with it. This is what makes "udp vs tcp" an
	// explicit, verifiable client choice rather than a fallback.
	if hello.Transport != s.tr {
		_ = s.send(proto.FrameAuthErr, proto.EncodeError(proto.ErrCodeBadRequest,
			fmt.Sprintf("transport mismatch: reached us over %s but requested %s", s.tr, hello.Transport)))
		return fmt.Errorf("transport mismatch: socket %s, hello %s", s.tr, hello.Transport)
	}

	s.internet = hello.Flags&proto.HelloFlagInternet != 0 && s.srv.gw != nil
	welcome := &proto.Welcome{
		IP:         ip,
		Prefix:     netw.Prefix(),
		MTU:        1400,
		NetworkID:  netw.ID,
		MaxMembers: uint16(s.srv.reg.Config().MaxMembers),
		Transport:  s.tr,
		Gateway:    s.internet,
	}
	if err := s.send(proto.FrameAuthOK, ok.Done()); err != nil {
		return err
	}
	if err := s.send(proto.FrameWelcome, welcome.Marshal()); err != nil {
		return err
	}

	s.started = time.Now()
	s.lastSeen.Store(time.Now().UnixNano())
	s.srv.sts.Sessions.Add(1)
	s.srv.cfg.Logf("session %s: authenticated %q on network %q as %s via %s (mtu=%d)",
		s.remoteString(), hello.Member, netw.Name, ipString(ip), s.tr, welcome.MTU)

	s.broadcastPeers()
	return nil
}

func splitProof(body []byte) (proof, wrapped []byte, err error) {
	if len(body) < 32 {
		return nil, nil, proto.ErrShortBuffer
	}
	return body[:32], body[32:], nil
}

// loop is the steady state: relay frames until the session ends.
func (s *Session) loop() {
	for {
		select {
		case <-s.done:
			return
		default:
		}
		if s.tr == proto.TransportTCP {
			_ = s.conn.SetReadDeadline(time.Now().Add(s.srv.cfg.PeerTimeout))
		}
		typ, body, err := s.receive()
		if err != nil {
			s.close(fmt.Sprintf("read error: %v", err))
			return
		}
		switch typ {
		case proto.FrameData:
			s.forward(body)
		case proto.FramePing:
			_ = s.send(proto.FramePong, body)
		case proto.FramePong:
			// liveness already refreshed by receive()
		case proto.FrameBye:
			s.close("client bye")
			return
		case proto.FrameHello, proto.FrameAuthProof:
			// A UDP client retransmitting its handshake after we authenticated
			// it: resend the tail of the handshake so it can complete.
			s.resendHandshakeTail()
		default:
			s.srv.sts.Dropped.Add(1)
		}
	}
}

// resendHandshakeTail re-sends AuthOK/Welcome for a UDP client that lost the
// final handshake datagrams.
func (s *Session) resendHandshakeTail() {
	if s.member == nil || s.network == nil {
		return
	}
	welcome := &proto.Welcome{
		IP:         s.member.IP,
		Prefix:     s.network.Prefix(),
		MTU:        1400,
		NetworkID:  s.network.ID,
		MaxMembers: uint16(s.srv.reg.Config().MaxMembers),
		Transport:  s.tr,
		Gateway:    s.internet,
	}
	_ = s.send(proto.FrameWelcome, welcome.Marshal())
	s.listMu.Lock()
	list := s.lastPeerList
	s.listMu.Unlock()
	if list != nil {
		_ = s.send(proto.FramePeerList, list)
	}
}

// forward relays one data frame to its destination inside the same network.
func (s *Session) forward(body []byte) {
	dst, pkt, err := proto.DecodeData(body)
	if err != nil {
		s.srv.sts.Dropped.Add(1)
		return
	}
	inNetwork := dst&0xffffff00 == s.network.subnet
	if !inNetwork {
		// Outside the own /24 is only allowed when the client enabled internet
		// access and the relay runs a gateway TUN device. Cross-network traffic
		// is impossible: the destination is resolved by IP in the global index
		// and its session then re-checks that the address belongs to it.
		if !s.internet || s.srv.gw == nil {
			s.srv.sts.Dropped.Add(1)
			return
		}
		s.rxBytes.Add(uint64(len(pkt)))
		s.srv.sts.RxBytes.Add(uint64(len(pkt)))
		s.srv.sts.TxBytes.Add(uint64(len(pkt)))
		if s.srv.cfg.Debug {
			s.srv.cfg.Logf("data: %s -> %s (%d bytes) via gateway", ipString(s.member.IP), ipString(dst), len(pkt))
		}
		if err := s.srv.gw.write(pkt); err != nil {
			s.srv.sts.Dropped.Add(1)
		}
		return
	}
	s.network.mu.RLock()
	m := s.network.members[dst]
	s.network.mu.RUnlock()
	if m == nil || m.Session == s {
		s.srv.sts.Dropped.Add(1)
		return
	}
	if s.srv.cfg.Debug {
		s.srv.cfg.Logf("data: %s -> %s (%d bytes) relayed in network %q", ipString(s.member.IP), ipString(dst), len(pkt), s.network.Name)
	}
	s.rxBytes.Add(uint64(len(pkt)))
	s.srv.sts.RxBytes.Add(uint64(len(pkt)))
	m.Session.txBytes.Add(uint64(len(pkt)))
	if err := m.Session.send(proto.FrameData, body); err != nil {
		s.srv.sts.Dropped.Add(1)
	}
	s.srv.sts.TxBytes.Add(uint64(len(pkt)))
}

// broadcastPeers pushes a fresh member list to every member of the network.
func (s *Session) broadcastPeers() {
	netw := s.network
	if netw == nil {
		return
	}
	members := netw.Members()
	for _, m := range members {
		pl := &proto.PeerList{}
		for _, o := range members {
			flags := uint8(0)
			if o.Session == m.Session {
				flags |= proto.PeerFlagSelf
			}
			pl.Peers = append(pl.Peers, proto.Peer{IP: o.IP, Name: o.Name, Transport: o.Session.tr, Flags: flags})
		}
		body := pl.Marshal()
		if m.Session == s {
			s.listMu.Lock()
			s.lastPeerList = body
			s.listMu.Unlock()
		}
		_ = m.Session.send(proto.FramePeerList, body)
	}
}

// runKeepalive sends pings and drops silent peers (used for UDP).
func (s *Session) runKeepalive(stop <-chan struct{}) {
	t := time.NewTicker(s.srv.cfg.Keepalive)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-s.done:
			return
		case <-t.C:
			_ = s.send(proto.FramePing, proto.EncodePing(uint64(time.Now().UnixNano())))
			last := time.Unix(0, s.lastSeen.Load())
			if time.Since(last) > s.srv.cfg.PeerTimeout {
				s.close("peer timed out")
				return
			}
		}
	}
}
