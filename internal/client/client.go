package client

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/zigorminsk-debug/cslvpn/internal/crypto"
	"github.com/zigorminsk-debug/cslvpn/internal/proto"
	"github.com/zigorminsk-debug/cslvpn/internal/tun"
)

// Options configures a tunnel client.
type Options struct {
	Server    string // host:port of the relay (the same port serves UDP and TCP)
	Transport proto.Transport
	Network   string
	Password  string
	Member    string
	Create    bool   // create the network on first join (like "Create network")
	WantIP    string // optional requested virtual address
	Iface     string // TUN interface name
	MTU       int    // tunnel MTU
	Internet  bool   // route all traffic (default route) through the relay
	Logger    *log.Logger
	Verbose   bool
	Debug     bool // log every packet (verbose, for troubleshooting)

	// Hooks (used by the CLI/TUI and tests).
	OnConnect  func(w *proto.Welcome, self string)
	OnPeers    func([]proto.Peer)
	OnRejected func(err error)
}

// Device is the minimal TUN surface the tunnel needs.
type Device interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
	Name() string
}

// Stats are live counters of the client.
type Stats struct {
	RxPackets  atomic.Uint64
	TxPackets  atomic.Uint64
	RxBytes    atomic.Uint64
	TxBytes    atomic.Uint64
	RTT        atomic.Int64 // last measured round-trip time, nanoseconds
	Reconnects atomic.Uint64
}

// Client is a single tunnel client.
type Client struct {
	opt Options
	log *log.Logger
	dev Device
	sts Stats

	mu      sync.Mutex
	link    link
	welcome *proto.Welcome
	ip      uint32

	peers atomic.Value // []proto.Peer

	closed    atomic.Bool
	closeOnce sync.Once
	done      chan struct{}

	// DeviceFactory is overridable for tests (netns-less unit tests).
	DeviceFactory func(name string, mtu int) (Device, error)
	// ConfigNetwork is called after the device exists; overridable for tests.
	ConfigNetwork func(d Device, w *proto.Welcome) error
}

// New creates a client.
func New(opt Options) *Client {
	if opt.Logger == nil {
		opt.Logger = log.Default()
	}
	if opt.MTU == 0 {
		opt.MTU = 1400
	}
	if opt.Iface == "" {
		opt.Iface = "cslvpn0"
	}
	if opt.Member == "" {
		if h, err := os.Hostname(); err == nil && h != "" {
			opt.Member = h
		} else {
			opt.Member = "cslvpn-client"
		}
	}
	c := &Client{opt: opt, log: opt.Logger, done: make(chan struct{})}
	c.DeviceFactory = func(name string, mtu int) (Device, error) {
		d, err := tun.Create(name)
		if err != nil {
			return nil, err
		}
		return d, nil
	}
	c.ConfigNetwork = configureLinuxIface
	return c
}

// Options returns the client options.
func (c *Client) Options() Options { return c.opt }

// Stats returns the live counters.
func (c *Client) Stats() *Stats { return &c.sts }

// Welcome returns the last welcome frame (nil before the first connection).
func (c *Client) Welcome() *proto.Welcome {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.welcome
}

// Peers returns the last received peer list.
func (c *Client) Peers() []proto.Peer {
	if v := c.peers.Load(); v != nil {
		return v.([]proto.Peer)
	}
	return nil
}

// Done is closed when the client stops for good.
func (c *Client) Done() <-chan struct{} { return c.done }

// Close stops the client.
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		close(c.done)
		c.mu.Lock()
		if c.link != nil {
			_ = c.link.close()
		}
		c.mu.Unlock()
		if c.dev != nil {
			_ = c.dev.Close()
		}
	})
}

// Run connects and reconnects until Close is called.
func (c *Client) Run() error {
	backoff := time.Second
	for {
		if c.closed.Load() {
			return nil
		}
		start := time.Now()
		err := c.session()
		if c.closed.Load() {
			return nil
		}
		if err != nil {
			c.log.Printf("session ended: %v", err)
			var ae *AuthError
			if errors.As(err, &ae) {
				if c.opt.OnRejected != nil {
					c.opt.OnRejected(err)
				}
				// Only permanent problems stop the client. "network not found"
				// is retried, because the network owner may still be creating it
				// (exactly like waiting for somebody else to create a Radmin
				// network and share the name/password).
				if ae.Code != proto.ErrCodeNoSuchNetwork && ae.Code != proto.ErrCodeNetworkBusy {
					return err
				}
			}
		}
		if time.Since(start) > 60*time.Second {
			backoff = time.Second
		}
		c.sts.Reconnects.Add(1)
		c.log.Printf("reconnecting in %s", backoff.Round(time.Second))
		select {
		case <-c.done:
			return nil
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// session performs one handshake and then pumps packets until it breaks.
func (c *Client) session() error {
	l, err := dial(c.opt.Server, c.opt.Transport)
	if err != nil {
		return fmt.Errorf("dial %s/%s: %w", c.opt.Server, c.opt.Transport, err)
	}
	defer l.close()
	c.mu.Lock()
	c.link = l
	c.mu.Unlock()

	priv, err := crypto.GenerateKeyPair()
	if err != nil {
		return err
	}
	clientPub := crypto.PublicKeyBytes(priv)
	cliNonce := crypto.RandomBytes(crypto.NonceLen)

	var want uint32
	if c.opt.WantIP != "" {
		if want, err = parseIP(c.opt.WantIP); err != nil {
			return err
		}
	}
	var flags uint8
	if c.opt.Create {
		flags |= proto.HelloFlagCreate
	}
	if c.opt.Internet {
		flags |= proto.HelloFlagInternet
	}
	// Requests for internet access but a relay without a gateway: fail early
	// instead of silently black-holing the default route.
	hello := &proto.Hello{
		Version:   proto.Version,
		Transport: c.opt.Transport,
		Flags:     flags,
		ClientPub: clientPub,
		Network:   c.opt.Network,
		Member:    c.opt.Member,
		DesiredIP: want,
	}
	copy(hello.ClientNonce[:], cliNonce)
	helloBytes := hello.Marshal()

	// 1. Hello -> Challenge (retransmitted for UDP, so a lost datagram is fine).
	challenge, err := c.exchangeChallenge(l, helloBytes)
	if err != nil {
		return err
	}
	if challenge.Version != proto.Version {
		return fmt.Errorf("relay speaks protocol v%d, client v%d", challenge.Version, proto.Version)
	}
	chBytes := challenge.Marshal()

	// 2. X25519 + key schedule.
	shared, err := crypto.SharedSecret(priv, challenge.ServerPub)
	if err != nil {
		return err
	}
	transcript := crypto.Transcript(helloBytes, chBytes, shared)
	pwKey := crypto.PasswordKey(c.opt.Network, c.opt.Password)
	wrapped, err := crypto.WrapForHandshake(shared, challenge.Salt[:], pwKey)
	if err != nil {
		return err
	}
	proof := crypto.ClientProof(pwKey, transcript, shared)
	proofFrame := append(append([]byte(nil), proof...), wrapped...)

	if err := l.write(proto.FrameAuthProof, proofFrame); err != nil {
		return err
	}

	// 3. Secure channel.
	c2s, s2c := crypto.SessionKeys(shared, pwKey, transcript)
	sendC, err := crypto.NewCipher(c2s)
	if err != nil {
		return err
	}
	recvC, err := crypto.NewCipher(s2c)
	if err != nil {
		return err
	}
	sc := newSecure(l, sendC, recvC)

	_ = l.setDeadline(time.Now().Add(20 * time.Second))
	w, err := c.finishHandshake(sc, transcript, shared, pwKey)
	if err != nil {
		return err
	}
	_ = l.setDeadline(time.Time{})

	c.mu.Lock()
	c.welcome = w
	c.ip = w.IP
	c.mu.Unlock()

	self := ipString(w.IP)
	if c.opt.Internet && !w.Gateway {
		return errors.New("the relay does not provide internet access (start it with -gateway)")
	}
	c.log.Printf("connected to %s via %s: network %q, member %q, address %s/%d, MTU %d",
		l.remote(), w.Transport, c.opt.Network, c.opt.Member, self, w.Prefix, w.MTU)
	if w.Gateway {
		c.log.Printf("internet access through the relay is enabled for this session")
	}
	if c.opt.OnConnect != nil {
		c.opt.OnConnect(w, self)
	}

	// 4. TUN device (created and configured once, survives reconnects).
	if c.dev == nil {
		dev, err := c.DeviceFactory(c.opt.Iface, int(w.MTU))
		if err != nil {
			return fmt.Errorf("tun: %w", err)
		}
		if c.ConfigNetwork != nil {
			if err := c.ConfigNetwork(dev, w); err != nil {
				c.log.Printf("warning: could not configure interface automatically: %v", err)
			}
			if w.Gateway && c.opt.Internet {
				host, _, _ := net.SplitHostPort(c.opt.Server)
				if err := tun.PinServerAndDefault(dev.Name(), host); err != nil {
					c.log.Printf("warning: could not install the default route: %v", err)
				} else {
					c.log.Printf("default route installed through %s, relay %s pinned outside the tunnel", dev.Name(), host)
				}
			}
		}
		c.dev = dev
	}

	// 5. Pump until the connection dies or we are closed.
	return c.pump(sc)
}

// finishHandshake waits for AuthOK and Welcome.
func (c *Client) finishHandshake(sc *secure, transcript, shared, pwKey []byte) (*proto.Welcome, error) {
	clientProof := crypto.ClientProof(pwKey, transcript, shared)
	serverVerified := false
	for {
		typ, body, err := sc.read()
		if err != nil {
			return nil, fmt.Errorf("handshake: %w", err)
		}
		switch typ {
		case proto.FrameAuthErr:
			code, msg := proto.DecodeError(body)
			if msg == "" {
				msg = proto.ErrorCodeString(code)
			}
			return nil, &AuthError{Code: code, Message: msg}
		case proto.FrameAuthOK:
			expect := crypto.ServerProof(pwKey, transcript, shared, clientProof)
			if !crypto.Equal(expect, body) {
				return nil, errors.New("relay failed to prove knowledge of the network key")
			}
			serverVerified = true
		case proto.FrameWelcome:
			if !serverVerified {
				return nil, errors.New("relay sent welcome before proving the network key")
			}
			w, err := proto.UnmarshalWelcome(body)
			if err != nil {
				return nil, err
			}
			if w.Transport != c.opt.Transport {
				return nil, fmt.Errorf("relay confirmed %s but %s was requested", w.Transport, c.opt.Transport)
			}
			return w, nil
		}
	}
}

// exchangeChallenge sends Hello until a Challenge arrives.
func (c *Client) exchangeChallenge(l link, helloBytes []byte) (*proto.Challenge, error) {
	deadline := time.Now().Add(20 * time.Second)
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			// Exponential-ish retransmission, capped so it stays responsive.
			d := time.Duration(300+attempt*200) * time.Millisecond
			if d > 3*time.Second {
				d = 3 * time.Second
			}
			time.Sleep(d)
		}
		if time.Now().After(deadline) {
			return nil, errors.New("relay did not answer the handshake in time")
		}
		if err := l.write(proto.FrameHello, helloBytes); err != nil {
			return nil, err
		}
		_ = l.setDeadline(time.Now().Add(1500 * time.Millisecond))
		for {
			typ, body, err := l.read()
			if err != nil {
				if isTimeout(err) {
					break // retransmit
				}
				return nil, err
			}
			switch typ {
			case proto.FrameChallenge:
				return proto.UnmarshalChallenge(body)
			case proto.FrameAuthErr:
				code, msg := proto.DecodeError(body)
				return nil, &AuthError{Code: code, Message: msg}
			}
		}
	}
}

// pump moves IP packets between the TUN device and the tunnel.
func (c *Client) pump(sc *secure) error {
	stop := make(chan struct{})
	var once sync.Once
	shutdown := func(why string) {
		once.Do(func() {
			c.log.Printf("tunnel stopping: %s", why)
			close(stop)
		})
	}

	// writeFrame writes a frame and heals the UDP socket if the kernel lost the
	// route after the default route was moved into the tunnel (a "network is
	// unreachable" error would otherwise kill an otherwise healthy tunnel).
	frameWrite := func(typ uint8, body []byte) error {
		err := sc.write(typ, body)
		if err == nil {
			return nil
		}
		if ul, ok := sc.l.(*udpLink); ok && errors.Is(err, syscall.ENETUNREACH) {
			if ferr := ul.ensureLocalAddr(); ferr == nil {
				c.log.Printf("udp socket re-bound after route change, retrying")
				return sc.write(typ, body)
			}
		}
		return err
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // TUN -> relay
		defer wg.Done()
		buf := make([]byte, 65535)
		for {
			n, err := c.dev.Read(buf)
			if err != nil {
				shutdown("tun read: " + err.Error())
				return
			}
			if n < 20 {
				continue
			}
			dst := binary.BigEndian.Uint32(buf[16:20])
			if c.opt.Debug {
				c.log.Printf("packet out: %d bytes -> %s", n, ipString(dst))
			}
			body := make([]byte, 4+n)
			binary.BigEndian.PutUint32(body, dst)
			copy(body[4:], buf[:n])
			if err := frameWrite(proto.FrameData, body); err != nil {
				shutdown("relay write: " + err.Error())
				return
			}
			c.sts.TxPackets.Add(1)
			c.sts.TxBytes.Add(uint64(n))
		}
	}()

	wg.Add(1)
	go func() { // relay -> TUN
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case <-c.done:
				return
			default:
			}
			if c.opt.Transport == proto.TransportTCP {
				_ = sc.setDeadline(time.Now().Add(60 * time.Second))
			} else {
				_ = sc.setDeadline(time.Now().Add(time.Second))
			}
			typ, body, err := sc.read()
			if err != nil {
				if isTimeout(err) {
					continue
				}
				shutdown("relay read: " + err.Error())
				return
			}
			switch typ {
			case proto.FrameData:
				_, pkt, err := proto.DecodeData(body)
				if err != nil {
					continue
				}
				if c.opt.Debug {
					c.log.Printf("packet in: %d bytes", len(pkt))
				}
				if _, err := c.dev.Write(pkt); err != nil {
					shutdown("tun write: " + err.Error())
					return
				}
				c.sts.RxPackets.Add(1)
				c.sts.RxBytes.Add(uint64(len(pkt)))
			case proto.FramePeerList:
				pl, err := proto.UnmarshalPeerList(body)
				if err != nil {
					continue
				}
				c.peers.Store(pl.Peers)
				if c.opt.Verbose {
					for _, p := range pl.Peers {
						if p.Flags&proto.PeerFlagSelf == 0 {
							c.log.Printf("peer %q at %s via %s", p.Name, ipString(p.IP), p.Transport)
						}
					}
				}
				if c.opt.OnPeers != nil {
					c.opt.OnPeers(pl.Peers)
				}
			case proto.FramePing:
				_ = frameWrite(proto.FramePong, body)
			case proto.FramePong:
				if ts, err := proto.DecodePing(body); err == nil {
					c.sts.RTT.Store(int64(time.Since(time.Unix(0, int64(ts)))))
				}
			case proto.FrameKicked, proto.FrameBye:
				shutdown("relay closed the session")
				return
			}
		}
	}()

	wg.Add(1)
	go func() { // keepalive
		defer wg.Done()
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-c.done:
				return
			case <-t.C:
				if err := frameWrite(proto.FramePing, proto.EncodePing(uint64(time.Now().UnixNano()))); err != nil {
					shutdown("keepalive write: " + err.Error())
					return
				}
			}
		}
	}()

	<-stop
	_ = sc.l.close()
	wg.Wait()
	return errLinkDown
}

var errLinkDown = errors.New("tunnel link down")

// configureLinuxIface assigns the address received from the relay.
func configureLinuxIface(d Device, w *proto.Welcome) error {
	cfg := tun.IPConfig{
		Name:      d.Name(),
		IP:        ipString(w.IP),
		Prefix:    int(w.Prefix),
		MTU:       int(w.MTU),
		RouteCIDR: cidrString(w.IP, w.Prefix),
	}
	return tun.Configure(cfg)
}

func cidrString(ip uint32, prefix uint8) string {
	if prefix > 32 {
		prefix = 32
	}
	mask := ^(uint32(1)<<(32-prefix) - 1)
	base := ip & mask
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, base)
	return fmt.Sprintf("%s/%d", net.IP(b).String(), prefix)
}

func ipString(ip uint32) string {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, ip)
	return net.IP(b).String()
}

func parseIP(s string) (uint32, error) {
	ip := net.ParseIP(s).To4()
	if ip == nil {
		return 0, fmt.Errorf("bad IPv4 address %q", s)
	}
	return binary.BigEndian.Uint32(ip), nil
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// AuthError is a relay rejection carrying the protocol error code.
type AuthError struct {
	Code    uint8
	Message string
}

func (e *AuthError) Error() string { return "relay rejected the client: " + e.Message }
