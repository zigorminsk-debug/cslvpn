package relay

import (
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zigorminsk-debug/cslvpn/internal/crypto"
	"github.com/zigorminsk-debug/cslvpn/internal/proto"
)

// Errors returned by the registry.
var (
	ErrBadPassword = errors.New("bad network password")
	ErrNetworkFull = errors.New("network is full")
	ErrNoFreeAddr  = errors.New("no free virtual address")
	ErrBanned      = errors.New("member is banned")
	ErrNameTaken   = errors.New("member name already online")
	ErrNotFound    = errors.New("network not found")
	ErrLimit       = errors.New("server limit reached")
)

// Config holds relay runtime limits.
type Config struct {
	AllowCreate bool          // clients may create new networks on first join
	MaxMembers  int           // members per network
	MaxNetworks int           // total number of networks
	IdleNetwork time.Duration // keep an empty network this long before GC
	BanDuration time.Duration // how long a kicked member stays banned
	Keepalive   time.Duration // ping interval
	PeerTimeout time.Duration // drop a peer after this much silence
	Debug       bool          // log every forwarded packet
	Logf        func(format string, args ...any)
}

// DefaultConfig returns limits mirroring Radmin VPN behaviour (150 members,
// 100 Mbps soft cap is enforced by the OS, not here).
func DefaultConfig() Config {
	return Config{
		AllowCreate: true,
		MaxMembers:  150,
		MaxNetworks: 4096,
		IdleNetwork: 24 * time.Hour,
		BanDuration: 15 * time.Minute,
		Keepalive:   10 * time.Second,
		PeerTimeout: 45 * time.Second,
	}
}

// Member is one online client inside a network.
type Member struct {
	Session *Session
	IP      uint32
	Name    string
	Joined  time.Time
}

// Network is an isolated virtual LAN ("room" in Radmin terms).
type Network struct {
	ID      uint32
	Name    string
	Key     []byte // PBKDF2(password); the server needs it to read inner IP headers
	subnet  uint32
	prefix  uint8
	created time.Time

	mu      sync.RWMutex
	members map[uint32]*Member // by virtual IP
	names   map[string]*Member // lowercase member name -> member
	banned  map[string]time.Time
	emptyAt time.Time
}

// Prefix returns the subnet prefix length of the network.
func (n *Network) Prefix() uint8 { return n.prefix }

// Subnet returns the network address of the virtual LAN.
func (n *Network) Subnet() string { return fmt.Sprintf("%s/%d", ipString(n.subnet), n.prefix) }

// Members returns a snapshot of the online members.
func (n *Network) Members() []*Member {
	n.mu.RLock()
	defer n.mu.RUnlock()
	out := make([]*Member, 0, len(n.members))
	for _, m := range n.members {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IP < out[j].IP })
	return out
}

// MemberCount returns the number of online members.
func (n *Network) MemberCount() int {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return len(n.members)
}

// Registry keeps all networks of a relay server.
type Registry struct {
	cfg Config

	mu       sync.RWMutex
	networks map[string]*Network
	byID     map[uint32]*Network
	byIP     map[uint32]*Session // every virtual address in every network
	nextID   uint32
	subnets  map[uint32]bool
}

// NewRegistry creates an empty registry.
func NewRegistry(cfg Config) *Registry {
	def := DefaultConfig()
	if cfg.MaxMembers == 0 {
		cfg.MaxMembers = def.MaxMembers
	}
	if cfg.MaxNetworks == 0 {
		cfg.MaxNetworks = def.MaxNetworks
	}
	if cfg.IdleNetwork == 0 {
		cfg.IdleNetwork = def.IdleNetwork
	}
	if cfg.BanDuration == 0 {
		cfg.BanDuration = def.BanDuration
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Registry{
		cfg:      cfg,
		networks: make(map[string]*Network),
		byID:     make(map[uint32]*Network),
		byIP:     make(map[uint32]*Session),
		nextID:   1,
		subnets:  make(map[uint32]bool),
	}
}

// Config returns a copy of the registry configuration.
func (r *Registry) Config() Config { return r.cfg }

// Networks returns a snapshot of all networks.
func (r *Registry) Networks() []*Network {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Network, 0, len(r.networks))
	for _, n := range r.networks {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Lookup finds a network by name (case-insensitive).
func (r *Registry) Lookup(name string) *Network {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.networks[strings.ToLower(name)]
}

// CreateNetwork creates (or returns) a network with the given password. It is
// used both by clients on first join and by the admin API.
func (r *Registry) CreateNetwork(name, password string) (*Network, error) {
	// Administrative creation always works, even when clients are not allowed
	// to create networks (-allow-create=false).
	return r.join1(name, crypto.PasswordKey(name, password), true, true)
}

func (r *Registry) join1(name string, key []byte, create, force bool) (*Network, error) {
	lname := strings.ToLower(name)
	if lname == "" {
		return nil, errors.New("network name must not be empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if n := r.networks[lname]; n != nil {
		return n, nil
	}
	if !create || (!r.cfg.AllowCreate && !force) {
		return nil, ErrNotFound
	}
	if len(r.networks) >= r.cfg.MaxNetworks {
		return nil, ErrLimit
	}
	base, err := r.allocSubnetLocked()
	if err != nil {
		return nil, err
	}
	n := &Network{
		ID:      r.nextID,
		Name:    name,
		Key:     key,
		subnet:  base,
		prefix:  24,
		created: time.Now(),
		members: make(map[uint32]*Member),
		names:   make(map[string]*Member),
		banned:  make(map[string]time.Time),
	}
	r.nextID++
	r.networks[lname] = n
	r.byID[n.ID] = n
	r.cfg.Logf("network %q created: id=%d subnet=%s", name, n.ID, n.Subnet())
	return n, nil
}

// Carve /24 blocks out of 26.0.0.0/8, the same address family Radmin VPN hands
// out (26.x.x.x), so users can keep the habit of typing 26.x.y.z.
func (r *Registry) allocSubnetLocked() (uint32, error) {
	for a := 0; a < 256; a++ {
		for b := 0; b < 256; b++ {
			base := binary.BigEndian.Uint32([]byte{26, byte(a), byte(b), 0})
			if !r.subnets[base] {
				r.subnets[base] = true
				return base, nil
			}
		}
	}
	return 0, ErrLimit
}

// Attach performs the final step of the handshake: it validates the network
// key, allocates a virtual address and registers the session.
func (r *Registry) Attach(hello *proto.Hello, key []byte, s *Session) (*Network, uint32, error) {
	create := hello.Flags&proto.HelloFlagCreate != 0
	netw, err := r.join1(hello.Network, key, create, false)
	if netw != nil && err == nil {
		// Existing network: the offered key must match the stored one.
		if subtle.ConstantTimeCompare(netw.Key, key) != 1 {
			return nil, 0, ErrBadPassword
		}
	}
	if err != nil {
		return nil, 0, err
	}

	netw.mu.Lock()
	defer netw.mu.Unlock()

	nameKey := strings.ToLower(hello.Member)
	if t, ok := netw.banned[nameKey]; ok {
		if time.Since(t) < r.cfg.BanDuration {
			return nil, 0, ErrBanned
		}
		delete(netw.banned, nameKey)
	}
	if prev, ok := netw.names[nameKey]; ok {
		if hello.Flags&proto.HelloFlagNoRetry != 0 {
			return nil, 0, ErrNameTaken
		}
		// Same person reconnecting (e.g. after a network drop on the relay
		// side): replace the stale registration instead of failing.
		r.cfg.Logf("network %q: replacing stale session for %q (%s)", netw.Name, prev.Name, ipString(prev.IP))
		go prev.Session.Close("replaced by a new session")
		delete(netw.members, prev.IP)
		delete(netw.names, nameKey)
	}
	if len(netw.members) >= r.cfg.MaxMembers {
		return nil, 0, ErrNetworkFull
	}

	ip, err := netw.allocLocked(hello.DesiredIP)
	if err != nil {
		return nil, 0, err
	}
	m := &Member{Session: s, IP: ip, Name: hello.Member, Joined: time.Now()}
	netw.members[ip] = m
	netw.names[nameKey] = m
	netw.emptyAt = time.Time{}
	s.member = m
	s.network = netw
	r.mu.Lock()
	r.byIP[ip] = s
	r.mu.Unlock()
	r.cfg.Logf("network %q: %q joined as %s via %s (%d/%d)", netw.Name, hello.Member, ipString(ip), s.Transport(), len(netw.members), r.cfg.MaxMembers)
	return netw, ip, nil
}

// allocLocked picks a free host address inside the /24 (x.x.x.2 - x.x.x.254).
func (n *Network) allocLocked(want uint32) (uint32, error) {
	if want != 0 && want&0xffffff00 == n.subnet {
		if _, used := n.members[want]; !used {
			return want, nil
		}
	}
	for host := uint32(2); host <= 254; host++ {
		ip := n.subnet | host
		if _, used := n.members[ip]; !used {
			return ip, nil
		}
	}
	return 0, ErrNoFreeAddr
}

// Detach removes a session from its network.
func (r *Registry) Detach(s *Session) {
	netw := s.network
	if netw == nil {
		return
	}
	netw.mu.Lock()
	if m, ok := netw.members[s.member.IP]; ok && m.Session == s {
		delete(netw.members, m.IP)
		delete(netw.names, strings.ToLower(m.Name))
		if len(netw.members) == 0 {
			netw.emptyAt = time.Now()
		}
	}
	count := len(netw.members)
	netw.mu.Unlock()
	r.mu.Lock()
	if cur, ok := r.byIP[s.member.IP]; ok && cur == s {
		delete(r.byIP, s.member.IP)
	}
	r.mu.Unlock()
	r.cfg.Logf("network %q: session %s detached (%d online)", netw.Name, ipString(s.member.IP), count)
}

// SessionByIP finds the session that owns a virtual address, across all
// networks. It is used to deliver internet-bound return traffic from the
// gateway TUN device.
func (r *Registry) SessionByIP(ip uint32) *Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byIP[ip]
}

// Kick removes a member and bans its name for BanDuration.
func (r *Registry) Kick(netw *Network, name string) bool {
	netw.mu.Lock()
	m, ok := netw.names[strings.ToLower(name)]
	if !ok {
		netw.mu.Unlock()
		return false
	}
	netw.banned[strings.ToLower(name)] = time.Now()
	delete(netw.members, m.IP)
	delete(netw.names, strings.ToLower(name))
	empty := len(netw.members) == 0
	if empty {
		netw.emptyAt = time.Now()
	}
	netw.mu.Unlock()
	r.mu.Lock()
	if cur, ok := r.byIP[m.IP]; ok && cur == m.Session {
		delete(r.byIP, m.IP)
	}
	r.mu.Unlock()
	go m.Session.Close("kicked by the relay administrator")
	return true
}

// GC drops networks that stayed empty longer than IdleNetwork.
func (r *Registry) GC() {
	cut := time.Now().Add(-r.cfg.IdleNetwork)
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, n := range r.networks {
		n.mu.RLock()
		empty := len(n.members) == 0 && !n.emptyAt.IsZero() && n.emptyAt.Before(cut)
		n.mu.RUnlock()
		if empty {
			delete(r.networks, k)
			delete(r.byID, n.ID)
			delete(r.subnets, n.subnet)
			r.cfg.Logf("network %q expired (idle)", n.Name)
		}
	}
}

// RunGC periodically garbage collects idle networks until stop is closed.
func (r *Registry) RunGC(stop <-chan struct{}) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			r.GC()
		}
	}
}

func ipString(ip uint32) string {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, ip)
	return net.IP(b).String()
}

// ParseIP converts a dotted quad to the internal uint32 form.
func ParseIP(s string) (uint32, error) {
	ip := net.ParseIP(s).To4()
	if ip == nil {
		return 0, fmt.Errorf("not an IPv4 address: %q", s)
	}
	return binary.BigEndian.Uint32(ip), nil
}
