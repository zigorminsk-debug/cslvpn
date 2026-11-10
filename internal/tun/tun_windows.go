//go:build windows

package tun

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

// TunInterface abstracts the platform TUN device so the tunnel core stays
// portable.
type TunInterface interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
	Name() string
}

// Device is the Windows TUN adapter handle.
type Device struct {
	Name string
	mu   sync.Mutex
	f    *os.File
	dev  TunInterface
}

// Create opens a Windows TUN adapter.
//
// Two ways to obtain the device are supported:
//
//  1. CSLVPN_TUN_FILE=<path> - an already opened Wintun/OpenVPN-TAP handle
//     passed by the GUI/service host (advanced deployments).
//  2. Otherwise a clear error is returned: the pure-Go build cannot create
//     NDIS adapters without the Wintun driver present. Install the optional
//     "wintun" driver package (see docs/WINDOWS.md) so Create can load it.
//
// The relay, handshake and everything else is fully functional on Windows;
// only the adapter creation needs the driver.
func Create(name string) (*Device, error) {
	if name == "" {
		name = "CSLVPN"
	}
	if path := os.Getenv("CSLVPN_TUN_FILE"); path != "" {
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return nil, fmt.Errorf("open CSLVPN_TUN_FILE: %w", err)
		}
		return &Device{Name: name, f: f}, nil
	}
	return nil, errors.New("windows TUN adapter not configured: install the Wintun driver and set CSLVPN_TUN_FILE, see docs/WINDOWS.md")
}

// Open attaches to an already configured adapter.
func Open(name string) (*Device, error) { return Create(name) }

func (d *Device) Read(p []byte) (int, error) {
	if d.dev != nil {
		return d.dev.Read(p)
	}
	return d.f.Read(p)
}

func (d *Device) Write(p []byte) (int, error) {
	if d.dev != nil {
		return d.dev.Write(p)
	}
	return d.f.Write(p)
}

func (d *Device) Close() error {
	if d.dev != nil {
		return d.dev.Close()
	}
	if d.f == nil {
		return nil
	}
	return d.f.Close()
}
