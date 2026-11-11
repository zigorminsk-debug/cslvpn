//go:build linux

// Package tun provides a minimal TUN device implementation. Linux uses the
// /dev/net/tun ioctl interface directly (no cgo, no external dependencies).
package tun

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	iffTun    = 0x0001
	iffNoPI   = 0x1000 // never prepend the 4-byte packet information header
	tunSetIFF = 0x400454ca
)

// Device is a TUN interface. Read/Write exchange raw IP packets.
type Device struct {
	name string
	f    *os.File
}

// Name returns the interface name.
func (d *Device) Name() string { return d.name }

// Create opens (and, with persistence, creates) a TUN interface named name.
// The interface is left administratively DOWN; the caller configures it with
// `ip link set`, `ip addr add`, `ip route add`.
func Create(name string) (*Device, error) {
	if name == "" {
		name = "tun0"
	}
	f, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open /dev/net/tun: %w (load the tun module and run as root)", err)
	}
	var ifr [40]byte
	copy(ifr[:16], name)
	*(*uint16)(unsafe.Pointer(&ifr[16])) = iffTun | iffNoPI
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(tunSetIFF), uintptr(unsafe.Pointer(&ifr[0])))
	if errno != 0 {
		f.Close()
		return nil, fmt.Errorf("TUNSETIFF %q: %w", name, errno)
	}
	actual := string(ifr[:16])
	if i := indexByte(actual, 0); i >= 0 {
		actual = actual[:i]
	}
	return &Device{name: actual, f: f}, nil
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// Open attaches to an existing persistent TUN interface (created outside of
// cslvpn, e.g. "ip tuntap add mode tun dev cslgw").
func Open(name string) (*Device, error) {
	return Create(name)
}

// Read reads one IP packet.
func (d *Device) Read(p []byte) (int, error) { return d.f.Read(p) }

// Write writes one IP packet.
func (d *Device) Write(p []byte) (int, error) { return d.f.Write(p) }

// Close closes the device.
func (d *Device) Close() error { return d.f.Close() }
