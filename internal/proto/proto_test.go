package proto

import (
	"bytes"
	"net"
	"testing"
)

func TestFrameRoundTripTCP(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, FrameData, []byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(&buf, FramePing, nil); err != nil {
		t.Fatal(err)
	}
	fr := NewFrameReader(&buf)
	typ, body, err := fr.Next()
	if err != nil || typ != FrameData || string(body) != "payload" {
		t.Fatalf("first frame: %d %q %v", typ, body, err)
	}
	typ, body, err = fr.Next()
	if err != nil || typ != FramePing || len(body) != 0 {
		t.Fatalf("second frame: %d %q %v", typ, body, err)
	}
}

func TestFrameDatagram(t *testing.T) {
	pkt := AppendFrame(nil, FrameData, []byte("abc"))
	typ, body, n, err := ParseFrame(pkt)
	if err != nil || typ != FrameData || string(body) != "abc" || n != len(pkt) {
		t.Fatalf("parse: %d %q %d %v", typ, body, n, err)
	}
	if _, _, _, err := ParseFrame(pkt[:2]); err == nil {
		t.Fatal("short datagram accepted")
	}
	// Oversized length must be rejected, not allocated.
	bad := []byte{0xff, 0xff, FrameData}
	if _, _, _, err := ParseFrame(bad); err == nil {
		t.Fatal("oversized frame accepted")
	}
}

func TestMessages(t *testing.T) {
	hello := &Hello{
		Version: Version, Transport: TransportUDP, Flags: HelloFlagCreate | HelloFlagInternet,
		Network: "office", Member: "alice", DesiredIP: 0x1a00000a,
	}
	hello.ClientPub[0] = 42
	got, err := UnmarshalHello(hello.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if got.Network != "office" || got.Member != "alice" || got.Transport != TransportUDP ||
		got.Flags != hello.Flags || got.DesiredIP != hello.DesiredIP || got.ClientPub[0] != 42 {
		t.Fatalf("hello round trip: %+v", got)
	}

	wel := &Welcome{IP: 0x1a000002, Prefix: 24, MTU: 1400, NetworkID: 7, MaxMembers: 150, Transport: TransportTCP, Gateway: true}
	w2, err := UnmarshalWelcome(wel.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if *w2 != *wel {
		t.Fatalf("welcome round trip: %+v != %+v", w2, wel)
	}

	pl := &PeerList{Peers: []Peer{
		{IP: 0x1a000002, Name: "alice", Transport: TransportUDP, Flags: PeerFlagSelf},
		{IP: 0x1a000003, Name: "bob", Transport: TransportTCP},
	}}
	p2, err := UnmarshalPeerList(pl.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Peers) != 2 || p2.Peers[1].Name != "bob" || p2.Peers[1].Transport != TransportTCP {
		t.Fatalf("peer list round trip: %+v", p2.Peers)
	}

	dst, pkt, err := DecodeData(EncodeData(0x1a000005, []byte{1, 2, 3}))
	if err != nil || dst != 0x1a000005 || !bytes.Equal(pkt, []byte{1, 2, 3}) {
		t.Fatalf("data frame: %v %v %v", dst, pkt, err)
	}

	ts, err := DecodePing(EncodePing(12345))
	if err != nil || ts != 12345 {
		t.Fatalf("ping: %d %v", ts, err)
	}

	code, msg := DecodeError(EncodeError(ErrCodeBadPassword, "nope"))
	if code != ErrCodeBadPassword || msg != "nope" {
		t.Fatalf("error frame: %d %q", code, msg)
	}

	for _, s := range []string{"udp", "tcp"} {
		if _, err := ParseTransport(s); err != nil {
			t.Fatalf("ParseTransport(%q): %v", s, err)
		}
	}
	if _, err := ParseTransport("sctp"); err == nil {
		t.Fatal("unknown transport accepted")
	}
}

func TestEncryptedFromWelcome(t *testing.T) {
	if IsEncrypted(FrameHello) || IsEncrypted(FrameChallenge) || IsEncrypted(FrameAuthProof) {
		t.Fatal("handshake frames must be plain")
	}
	for _, typ := range []uint8{FrameWelcome, FramePeerList, FrameData, FramePing, FrameKicked} {
		if !IsEncrypted(typ) {
			t.Fatalf("frame %d must be encrypted", typ)
		}
	}
}

func TestIPHelpers(t *testing.T) {
	ip := net.ParseIP("26.0.1.2").To4()
	if ip == nil {
		t.Fatal("bad test IP")
	}
}
