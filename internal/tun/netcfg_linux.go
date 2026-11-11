//go:build linux

package tun

import (
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
)

// IPConfig describes how a virtual network interface is configured.
type IPConfig struct {
	Name      string // interface name
	IP        string // virtual address, e.g. 26.12.7.5
	Prefix    int    // subnet prefix, e.g. 24
	MTU       int    // e.g. 1400
	RouteCIDR string // subnet routed through the device, e.g. 26.12.7.0/24
}

// Configure assigns the address/MTU and brings the interface up.
func Configure(cfg IPConfig) error {
	steps := [][]string{
		{"link", "set", "dev", cfg.Name, "mtu", strconv.Itoa(cfg.MTU), "up"},
		{"addr", "replace", cfg.IP + "/" + strconv.Itoa(cfg.Prefix), "dev", cfg.Name},
	}
	if cfg.RouteCIDR != "" {
		steps = append(steps, []string{"route", "replace", cfg.RouteCIDR, "dev", cfg.Name})
	}
	for _, s := range steps {
		if out, err := exec.Command("ip", s...).CombinedOutput(); err != nil {
			return fmt.Errorf("ip %s: %v: %s", strings.Join(s, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// Remove deletes the interface.
func Remove(name string) {
	_ = exec.Command("ip", "link", "del", "dev", name).Run()
}

// AddNAT turns the host into an internet gateway for the virtual network: it
// enables IP forwarding and masquerades traffic coming from the TUN device.
// This is the analogue of Radmin VPN's "internet access for the LAN", except
// that the NAT is executed by the relay server operator, not by a driver in
// every client.
func AddNAT(iface, cidr string) error {
	if out, err := exec.Command("sysctl", "-w", "net.ipv4.ip_forward=1").CombinedOutput(); err != nil {
		return fmt.Errorf("sysctl ip_forward: %v: %s", err, strings.TrimSpace(string(out)))
	}
	// Masquerade everything that comes from the virtual network but leaves
	// through a real uplink. When iface is empty, every egress interface is
	// matched ("-o" is omitted).
	nat := []string{"-t", "nat", "-A", "POSTROUTING", "-s", cidr}
	if iface != "" {
		nat = append(nat, "!", "-o", iface)
	}
	nat = append(nat, "-j", "MASQUERADE")
	rules := [][]string{nat}
	if iface != "" {
		rules = append(rules,
			[]string{"-A", "FORWARD", "-i", iface, "-j", "ACCEPT"},
			[]string{"-A", "FORWARD", "-o", iface, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		)
	} else {
		// Forwarding happens between the veth/uplink interfaces of the host,
		// the virtual addresses are bridged by the relay itself.
		rules = append(rules, []string{"-A", "FORWARD", "-s", cidr, "-j", "ACCEPT"})
	}
	for _, r := range rules {
		if out, err := exec.Command("iptables", r...).CombinedOutput(); err != nil {
			return fmt.Errorf("iptables %s: %v: %s", strings.Join(r, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// RemoveNAT removes the rules installed by AddNAT (best effort).
func RemoveNAT(iface, cidr string) {
	nat := []string{"-t", "nat", "-D", "POSTROUTING", "-s", cidr}
	if iface != "" {
		nat = append(nat, "!", "-o", iface)
	}
	nat = append(nat, "-j", "MASQUERADE")
	rules := [][]string{nat}
	if iface != "" {
		rules = append(rules,
			[]string{"-D", "FORWARD", "-i", iface, "-j", "ACCEPT"},
			[]string{"-D", "FORWARD", "-o", iface, "-m", "state", "--state", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		)
	} else {
		rules = append(rules, []string{"-D", "FORWARD", "-s", cidr, "-j", "ACCEPT"})
	}
	for _, r := range rules {
		_ = exec.Command("iptables", r...).Run()
	}
}

// RouteVia routes a subnet through a device (used by the relay so that the
// NATed return traffic from the internet finds the virtual network).
func RouteVia(cidr, iface string) error {
	if out, err := exec.Command("ip", "route", "replace", cidr, "dev", iface).CombinedOutput(); err != nil {
		return fmt.Errorf("ip route replace %s dev %s: %v: %s", cidr, iface, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SetDefaultRoute points the system default route into the tunnel. The old
// default route is replaced; run the client in a dedicated namespace or make
// sure you know how to get your traffic back (docs/WINDOWS.md has notes for
// Windows, where this is done with two `route` commands).
func SetDefaultRoute(iface string) error { return PinServerAndDefault(iface, "") }

// PinServerAndDefault keeps the tunnel's own transport reachable while the
// default route moves into the tunnel: the relay address gets a /32 route via
// the previous default gateway (the "VPN server bypass" every VPN client
// needs, otherwise the encrypted packets would be routed into the tunnel
// itself).
func PinServerAndDefault(iface, serverHost string) error {
	if serverHost != "" {
		out, err := exec.Command("ip", "route", "get", serverHost).CombinedOutput()
		if err == nil {
			line := strings.TrimSpace(string(out))
			// "203.0.113.10 via 192.168.1.1 dev eth0 src 192.168.1.20"
			fields := strings.Fields(line)
			var via, dev string
			for i := 0; i+1 < len(fields); i++ {
				switch fields[i] {
				case "via":
					via = fields[i+1]
				case "dev":
					dev = fields[i+1]
				}
			}
			args := []string{"route", "replace", serverHost + "/32"}
			if via != "" {
				args = append(args, "via", via)
			}
			if dev != "" {
				args = append(args, "dev", dev)
			}
			if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
				return fmt.Errorf("ip %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
			}
		}
	}
	if out, err := exec.Command("ip", "route", "replace", "default", "dev", iface).CombinedOutput(); err != nil {
		return fmt.Errorf("ip route replace default dev %s: %v: %s", iface, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// AddGatewayRoute routes a single subnet into the tunnel (used with split
// tunnelling: only the virtual network and any extra subnets go through it).
func AddGatewayRoute(cidr, iface string) error {
	if out, err := exec.Command("ip", "route", "replace", cidr, "dev", iface).CombinedOutput(); err != nil {
		return fmt.Errorf("ip route replace %s dev %s: %v: %s", cidr, iface, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SplitCIDR splits "26.12.7.0/24" into network address and prefix.
func SplitCIDR(s string) (string, int, error) {
	ip, ipnet, err := net.ParseCIDR(s)
	if err != nil {
		return "", 0, err
	}
	ones, _ := ipnet.Mask.Size()
	_ = ip
	return ipnet.IP.String(), ones, nil
}
