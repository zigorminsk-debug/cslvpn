//go:build windows

package tun

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

// Device is the Windows TUN adapter handle.
//
// Windows requires a driver (Wintun) to create an NDIS adapter, which the
// pure-Go build deliberately does not bundle. Everything else - handshake,
// encryption, transports, relay - is platform independent, so this file only
// implements the I/O half of the interface.
type Device struct {
	name string
	mu   sync.Mutex
	f    *os.File
}

// Create opens a Windows TUN adapter.
//
// Two ways to obtain a device are supported:
//
//  1. CSLVPN_TUN_FILE=<path> - an already opened Wintun/TAP handle passed in by
//     a GUI/service host or by the Wintun helper (see docs/WINDOWS.md).
//  2. Otherwise a descriptive error is returned instead of a panic, so the
//     tunnel client can print the exact next step.
func Create(name string) (*Device, error) {
	if name == "" {
		name = "CSLVPN"
	}
	if path := os.Getenv("CSLVPN_TUN_FILE"); path != "" {
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return nil, fmt.Errorf("open CSLVPN_TUN_FILE: %w", err)
		}
		return &Device{name: name, f: f}, nil
	}
	return nil, errors.New("no Windows TUN adapter: install the Wintun driver and set CSLVPN_TUN_FILE, or run the client inside WSL2 (see docs/WINDOWS.md)")
}

// Open attaches to an already configured adapter.
func Open(name string) (*Device, error) { return Create(name) }

// Name returns the adapter name.
func (d *Device) Name() string { return d.name }

// Read reads one IP packet.
func (d *Device) Read(p []byte) (int, error) {
	if d.f == nil {
		return 0, errors.New("tun device is not open")
	}
	return d.f.Read(p)
}

// Write writes one IP packet.
func (d *Device) Write(p []byte) (int, error) {
	if d.f == nil {
		return 0, errors.New("tun device is not open")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.f.Write(p)
}

// Close closes the adapter handle.
func (d *Device) Close() error {
	if d.f == nil {
		return nil
	}
	err := d.f.Close()
	d.f = nil
	return err
}
