//go:build !linux

package vsock

import "net"

// This file is the portable stub for every non-Linux GOOS. AF_VSOCK is a
// Linux address family, so the transport entry points return ErrUnsupported
// (or a zero value). They exist so cross-platform code compiles and vets
// without a build-tag fork of its own; the pure CID, address, and sockaddr
// logic in vsock.go still runs everywhere.

// Dial returns ErrUnsupported: AF_VSOCK is Linux-only.
func Dial(cid, port uint32) (Conn, error) {
	return nil, ErrUnsupported
}

// Listen returns ErrUnsupported: AF_VSOCK is Linux-only.
func Listen(port uint32) (net.Listener, error) {
	return nil, ErrUnsupported
}

// LocalCID returns 0: there is no AF_VSOCK CID on this platform.
func LocalCID() uint32 { return 0 }

// Supported reports false: this platform has no AF_VSOCK.
func Supported() bool { return false }
