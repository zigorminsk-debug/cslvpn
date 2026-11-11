//go:build !linux && !windows

// Fallback TUN implementation for platforms without a driver in this build
// (macOS, BSD). The tunnel core is platform independent; only the adapter needs
// a driver. Compiles everywhere, returns a clear error at runtime.
package tun

import "errors"

// Device is a placeholder adapter.
type Device struct{ name string }

// Create reports that no TUN driver is available in this build.
func Create(name string) (*Device, error) {
	return nil, errors.New("no TUN driver in this build for this platform: use Linux, Windows (Wintun) or run the client in a VM/container")
}

// Open reports that no TUN driver is available in this build.
func Open(name string) (*Device, error) { return Create(name) }

// Name returns the adapter name.
func (d *Device) Name() string { return d.name }

// Read always fails: no device.
func (d *Device) Read(p []byte) (int, error) { return 0, errors.New("tun device is not open") }

// Write always fails: no device.
func (d *Device) Write(p []byte) (int, error) { return 0, errors.New("tun device is not open") }

// Close is a no-op.
func (d *Device) Close() error { return nil }
