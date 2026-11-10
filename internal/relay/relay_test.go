package relay

import (
	"io"
	"log"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/zigorminsk-debug/cslvpn/internal/crypto"
	"github.com/zigorminsk-debug/cslvpn/internal/proto"
)

// hostPort rewrites a wildcard bind address ([::] or 0.0.0.0) to 127.0.0.1 so
// the tests always dial the IPv4 loopback listener.
func hostPort(t *testing.T, a net.Addr) string {
	t.Helper()
	host, port, err := net.SplitHostPort(a.String())
	if err != nil {
		t.Fatalf("bad listener address %q: %v", a, err)
	}
	if host == "" || host == "::" || host == "0.0.0.0" {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return net.JoinHostPort(host, port)
}

func testServer(t *testing.T) *Server {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PeerTimeout = 5 * time.Second
	cfg.Keepalive = time.Second
	cfg.Logf = t.Logf
	return NewServer(cfg, log.New(io.Discard, "", 0))
}

func TestRegistrySubnetsAndIsolation(t *testing.T) {
	r := NewRegistry(DefaultConfig())
	a, err := r.CreateNetwork("alpha", "pw")
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.CreateNetwork("beta", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if a.subnet == b.subnet {
		t.Fatal("two networks got the same subnet")
	}
	if again, err := r.CreateNetwork("Alpha", "other"); err != nil || again != a {
		t.Fatalf("names must be case insensitive and idempotent: %v", err)
	}
	if r.Lookup("beta") != b || r.Lookup("nope") != nil {
		t.Fatal("lookup by name failed")
	}
}

func TestRegistryAddressAllocation(t *testing.T) {
	r := NewRegistry(DefaultConfig())
	n, err := r.CreateNetwork("net", "pw")
	if err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	ip1, err := n.allocLocked(0)
	// Simulate two joins by occupying the address.
	n.members[ip1] = &Member{IP: ip1}
	ip2, err2 := n.allocLocked(0)
	n.mu.Unlock()
	if err != nil || err2 != nil {
		t.Fatal(err, err2)
	}
	if ip1 == ip2 {
		t.Fatal("allocated the same address twice")
	}
	if ip1&0xffffff00 != n.subnet || ip1&0xff < 2 {
		t.Fatalf("address %x outside the /24 host range", ip1)
	}
}

// TestRelayEndToEnd starts a relay and two real clients (TCP and UDP), which
// exchange an IP packet through the tunnel. No TUN device is needed: the test
// talks the protocol directly.
func TestRelayEndToEnd(t *testing.T) {
	for _, tr := range []proto.Transport{proto.TransportUDP, proto.TransportTCP} {
		tr := tr
		t.Run(tr.String(), func(t *testing.T) {
			srv := testServer(t)
			if err := srv.StartUDP("127.0.0.1:0"); err != nil {
				t.Fatal(err)
			}
			if err := srv.StartTCP("127.0.0.1:0"); err != nil {
				t.Fatal(err)
			}
			defer srv.Shutdown()
			// In production both listeners share one port; with ":0" the kernel
			// picks different ones, so address the right listener per transport.
			var addr string
			if tr == proto.TransportUDP {
				addr = hostPort(t, srv.UDPAddr())
			} else {
				addr = hostPort(t, srv.TCPAddr())
			}

			c1 := dialTestClient(t, addr, tr, "net", "pw", "one", true)
			defer c1.close()
			c2 := dialTestClient(t, addr, tr, "net", "pw", "two", false)
			defer c2.close()

			if c1.ip == c2.ip {
				t.Fatal("clients share an address")
			}

			// Wait for peer lists to settle.
			time.Sleep(300 * time.Millisecond)

			// Build a minimal IPv4 header (20 bytes) + payload.
			pkt := make([]byte, 28)
			pkt[0] = 0x45 // version 4, IHL 5
			pkt[8] = 64   // TTL
			pkt[9] = 1    // ICMP
			pkt[16], pkt[17], pkt[18], pkt[19] = byte(c1.ip>>24), byte(c1.ip>>16), byte(c1.ip>>8), byte(c1.ip)
			pkt[20], pkt[21], pkt[22], pkt[23] = byte(c2.ip>>24), byte(c2.ip>>16), byte(c2.ip>>8), byte(c2.ip)
			copy(pkt[24:], []byte("ping"))

			if err := c1.send(proto.FrameData, proto.EncodeData(c2.ip, pkt)); err != nil {
				t.Fatal(err)
			}
			typ, body, err := c2.recvData(3 * time.Second)
			if err != nil {
				t.Fatalf("client two received nothing: %v", err)
			}
			if typ != proto.FrameData {
				t.Fatalf("expected data frame, got %d", typ)
			}
			dst, got, err := proto.DecodeData(body)
			if err != nil {
				t.Fatal(err)
			}
			if dst != c2.ip {
				t.Fatalf("destination %x != %x", dst, c2.ip)
			}
			if string(got[24:]) != "ping" {
				t.Fatalf("payload corrupted: %q", got[24:])
			}

			// Traffic to a foreign network must be dropped by the relay.
			other := dialTestClient(t, addr, tr, "othernet", "pw", "three", true)
			defer other.close()
			if err := c1.send(proto.FrameData, proto.EncodeData(other.ip, pkt)); err != nil {
				t.Fatal(err)
			}
			if _, _, err := other.recvData(500 * time.Millisecond); err == nil {
				t.Fatal("traffic crossed network boundaries")
			}
		})
	}
}

func TestWrongPasswordRejected(t *testing.T) {
	srv := testServer(t)
	if err := srv.StartUDP("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown()
	addr := hostPort(t, srv.UDPAddr())

	owner := dialTestClient(t, addr, proto.TransportUDP, "net", "right", "owner", true)
	defer owner.close()

	c := &testClient{}
	err := c.connect(addr, proto.TransportUDP, "net", "wrong", "intruder", false)
	if err == nil {
		c.close()
		t.Fatal("a client with the wrong password was accepted")
	}
	ae, ok := err.(*authError)
	if !ok || ae.code != proto.ErrCodeBadPassword {
		t.Fatalf("expected bad password error, got %v", err)
	}
}

func TestNetworkCreationDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AllowCreate = false
	cfg.Logf = t.Logf
	srv := NewServer(cfg, log.New(io.Discard, "", 0))
	if err := srv.StartUDP("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown()

	// Joining without the create flag must never create a network.
	c := &testClient{}
	err := c.connect(hostPort(t, srv.UDPAddr()), proto.TransportUDP, "new", "pw", "x", false)
	if err == nil {
		c.close()
		t.Fatal("an unknown network was joined although creation is disabled")
	}
	if ae, ok := err.(*authError); !ok || ae.code != proto.ErrCodeNoSuchNetwork {
		t.Fatalf("unexpected error: %v", err)
	}

	// An admin pre-created network can still be joined without -create.
	if _, err := srv.reg.CreateNetwork("preexisting", "pw"); err != nil {
		t.Fatal(err)
	}
	c2 := dialTestClient(t, hostPort(t, srv.UDPAddr()), proto.TransportUDP, "preexisting", "pw", "y", false)
	c2.close()
}

func TestKickAndBan(t *testing.T) {
	srv := testServer(t)
	if err := srv.StartUDP("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown()
	addr := hostPort(t, srv.UDPAddr())

	victim := dialTestClient(t, addr, proto.TransportUDP, "net", "pw", "victim", true)
	defer victim.close()
	netw := srv.reg.Lookup("net")
	if netw == nil {
		t.Fatal("network missing")
	}
	if !srv.reg.Kick(netw, "victim") {
		t.Fatal("kick failed")
	}
	time.Sleep(200 * time.Millisecond)
	if netw.MemberCount() != 0 {
		t.Fatal("kicked member still registered")
	}
	// The banned name cannot immediately rejoin.
	c := &testClient{}
	err := c.connect(addr, proto.TransportUDP, "net", "pw", "victim", false)
	if err == nil {
		c.close()
		t.Fatal("kicked member rejoined immediately")
	}
	if ae, ok := err.(*authError); !ok || ae.code != proto.ErrCodeBanned {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTransportMismatchRejected(t *testing.T) {
	srv := testServer(t)
	if err := srv.StartUDP("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	defer srv.Shutdown()
	// Reach the UDP listener but claim TCP in the Hello.
	c := &testClient{}
	err := c.connect(hostPort(t, srv.UDPAddr()), proto.TransportTCP, "net", "pw", "x", true)
	if err == nil {
		c.close()
		t.Fatal("transport mismatch accepted")
	}
}

// ---- minimal test client -------------------------------------------------

type testClient struct {
	conn    net.Conn
	fr      *proto.FrameReader
	udp     *net.UDPConn
	sendC   *crypto.Cipher
	recvC   *crypto.Cipher
	sendSeq uint64
	recvSeq uint64
	ip      uint32
	mu      sync.Mutex
	pending [][2]any // buffered frames (typ, body)
}

type authError struct {
	code uint8
	msg  string
}

func (e *authError) Error() string { return proto.ErrorCodeString(e.code) + ": " + e.msg }

func (c *testClient) close() {
	if c.conn != nil {
		_ = c.conn.Close()
	}
	if c.udp != nil {
		_ = c.udp.Close()
	}
}

func (c *testClient) write(typ uint8, body []byte) error {
	if !proto.IsEncrypted(typ) {
		return c.writeRaw(typ, body)
	}
	seq := c.sendSeq
	c.sendSeq++
	sealed := c.sendC.Seal(seq, typ, body)
	out := append([]byte{byte(seq >> 24), byte(seq >> 16), byte(seq >> 8), byte(seq)}, sealed[1:]...)
	return c.writeRaw(typ, out)
}

func (c *testClient) writeRaw(typ uint8, body []byte) error {
	if c.udp != nil {
		_, err := c.udp.Write(proto.AppendFrame(nil, typ, body))
		return err
	}
	return proto.WriteFrame(c.conn, typ, body)
}

func (c *testClient) read(timeout time.Duration) (uint8, []byte, error) {
	if c.udp != nil {
		_ = c.udp.SetReadDeadline(time.Now().Add(timeout))
		buf := make([]byte, 65535)
		n, err := c.udp.Read(buf)
		if err != nil {
			return 0, nil, err
		}
		typ, body, _, err := proto.ParseFrame(buf[:n])
		if err != nil {
			return 0, nil, err
		}
		return c.decrypt(typ, body)
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(timeout))
	if c.fr == nil {
		c.fr = proto.NewFrameReader(c.conn)
	}
	typ, body, err := c.fr.Next()
	if err != nil {
		return 0, nil, err
	}
	return c.decrypt(typ, body)
}

func (c *testClient) decrypt(typ uint8, body []byte) (uint8, []byte, error) {
	if !proto.IsEncrypted(typ) {
		return typ, body, nil
	}
	seq := uint64(body[0])<<24 | uint64(body[1])<<16 | uint64(body[2])<<8 | uint64(body[3])
	t, pt, err := c.recvC.Open(seq, append([]byte{typ}, body[4:]...))
	if err != nil {
		return 0, nil, err
	}
	return t, pt, nil
}

func (c *testClient) send(typ uint8, body []byte) error { return c.write(typ, body) }

// recvData skips control frames and returns the next data frame.
func (c *testClient) recvData(timeout time.Duration) (uint8, []byte, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		typ, body, err := c.read(time.Until(deadline))
		if err != nil {
			return 0, nil, err
		}
		if typ == proto.FrameData {
			return typ, body, nil
		}
	}
	return 0, nil, io.EOF
}

func (c *testClient) connect(addr string, tr proto.Transport, network, password, member string, create bool) error {
	priv, err := crypto.GenerateKeyPair()
	if err != nil {
		return err
	}
	pub := crypto.PublicKeyBytes(priv)
	var flags uint8
	if create {
		flags |= proto.HelloFlagCreate
	}
	hello := &proto.Hello{Version: proto.Version, Transport: tr, Flags: flags, ClientPub: pub, Network: network, Member: member}
	helloBytes := hello.Marshal()

	switch tr {
	case proto.TransportUDP:
		ra, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			return err
		}
		uc, err := net.DialUDP("udp", nil, ra)
		if err != nil {
			return err
		}
		c.udp = uc
	default:
		cx, err := net.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			return err
		}
		c.conn = cx
	}

	// Hello -> Challenge (retry for UDP).
	var challenge *proto.Challenge
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := c.writeRaw(proto.FrameHello, helloBytes); err != nil {
			return err
		}
		typ, body, err := c.read(500 * time.Millisecond)
		if err != nil {
			if time.Now().After(deadline) {
				return err
			}
			continue
		}
		switch typ {
		case proto.FrameChallenge:
			challenge, err = proto.UnmarshalChallenge(body)
			if err != nil {
				return err
			}
		case proto.FrameAuthErr:
			code, msg := proto.DecodeError(body)
			return &authError{code: code, msg: msg}
		default:
			continue
		}
		break
	}

	shared, err := crypto.SharedSecret(priv, challenge.ServerPub)
	if err != nil {
		return err
	}
	transcript := crypto.Transcript(helloBytes, challenge.Marshal(), shared)
	pwKey := crypto.PasswordKey(network, password)
	wrapped, err := crypto.WrapForHandshake(shared, challenge.Salt[:], pwKey)
	if err != nil {
		return err
	}
	proof := crypto.ClientProof(pwKey, transcript, shared)
	if err := c.writeRaw(proto.FrameAuthProof, append(proof, wrapped...)); err != nil {
		return err
	}

	c2s, s2c := crypto.SessionKeys(shared, pwKey, transcript)
	if c.sendC, err = crypto.NewCipher(c2s); err != nil {
		return err
	}
	if c.recvC, err = crypto.NewCipher(s2c); err != nil {
		return err
	}

	// AuthOK then Welcome.
	gotWelcome := false
	for !gotWelcome {
		typ, body, err := c.read(5 * time.Second)
		if err != nil {
			return err
		}
		switch typ {
		case proto.FrameAuthErr:
			code, msg := proto.DecodeError(body)
			return &authError{code: code, msg: msg}
		case proto.FrameAuthOK:
			expect := crypto.ServerProof(pwKey, transcript, shared, proof)
			if !crypto.Equal(expect, body) {
				return &authError{code: proto.ErrCodeGeneric, msg: "bad server proof"}
			}
		case proto.FrameWelcome:
			w, err := proto.UnmarshalWelcome(body)
			if err != nil {
				return err
			}
			c.ip = w.IP
			gotWelcome = true
		}
	}
	return nil
}

func dialTestClient(t *testing.T, addr string, tr proto.Transport, network, password, member string, create bool) *testClient {
	t.Helper()
	c := &testClient{}
	if err := c.connect(addr, tr, network, password, member, create); err != nil {
		t.Fatalf("client %q: %v", member, err)
	}
	return c
}
