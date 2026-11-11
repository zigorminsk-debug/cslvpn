//go:build !linux

package tun

import (
	"errors"
	"strings"
)

// IPConfig describes how a virtual network interface is configured.
type IPConfig struct {
	Name      string
	IP        string
	Prefix    int
	MTU       int
	RouteCIDR string
}

// Configure is not available outside Linux in the pure-Go build. On Windows
// use `netsh interface ip set address "<Name>" static <ip> <mask>` in the
// service wrapper (see docs/WINDOWS.md), on macOS use
// `ifconfig <Name> inet <ip> <ip> alias; ifconfig <Name> mtu <mtu> up`.
func Configure(cfg IPConfig) error {
	return errors.New("automatic interface configuration is only implemented for linux in this build")
}

// Remove is a no-op on non-Linux platforms.
func Remove(name string) {}

// AddNAT is only implemented for Linux (iptables).
func AddNAT(iface, cidr string) error {
	return errors.New("server NAT is only implemented for linux")
}

// RemoveNAT is only implemented for Linux.
func RemoveNAT(iface, cidr string) {}

// SetDefaultRoute is not implemented outside Linux in this build.
func SetDefaultRoute(iface string) error {
	return errors.New("default route setup is only implemented for linux in this build")
}

// RouteVia is only implemented for Linux (iproute2).
func RouteVia(cidr, iface string) error {
	return errors.New("route setup is only implemented for linux in this build")
}

// PinServerAndDefault is not implemented outside Linux in this build.
func PinServerAndDefault(iface, serverHost string) error { return SetDefaultRoute(iface) }

// AddGatewayRoute is not implemented outside Linux in this build.
func AddGatewayRoute(cidr, iface string) error {
	return errors.New("route setup is only implemented for linux in this build")
}

// SplitCIDR splits "26.12.7.0/24" into network address and prefix.
func SplitCIDR(s string) (string, int, error) {
	i := strings.IndexByte(s, '/')
	if i < 0 {
		return "", 0, errors.New("missing prefix length")
	}
	var ones int
	for _, c := range s[i+1:] {
		if c < '0' || c > '9' {
			return "", 0, errors.New("bad prefix length")
		}
		ones = ones*10 + int(c-'0')
	}
	return s[:i], ones, nil
}
