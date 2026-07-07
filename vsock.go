// Package vsock is a pure-Go (CGO=0) AF_VSOCK transport: a net.Conn
// Dialer and a net.Listener over the Linux virtio-vsock address family,
// with no third-party dependencies.
//
// AF_VSOCK is the host<->guest socket family used by KVM/QEMU virtio-vsock
// and Apple Virtualization (VZVirtioSocketDevice). Both expose the same
// kernel interface to a Linux guest, so a single implementation serves
// either hypervisor. An address is a (context id, port) pair: the context
// id ("CID") names a VM, the port names a service inside it.
//
// It is designed to carry gRPC between a hypervisor host and its guest
// microVMs — the server's Listen returns connections you pass straight to
// grpc.Server.Serve, and Dialer.DialContext has the signature grpc's
// WithContextDialer expects — but nothing here depends on gRPC; the API is
// plain net.Conn / net.Listener.
//
// The transport is Linux-only. On every other GOOS the package still
// builds and vets: Dial, Listen, LocalCID and Supported are present but
// return ErrUnsupported (or their zero value), so cross-platform callers
// compile without a build-tag fork of their own.
package vsock

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// Conn is an established vsock connection. It is a net.Conn; connections
// returned by the server-side Listener additionally report an Addr from
// RemoteAddr and LocalAddr.
type Conn = net.Conn

// Reserved context identifiers from linux/vm_sockets.h. Real guest CIDs
// are 3 or higher; the values below are special and never assigned to a
// guest.
const (
	// CIDAny (VMADDR_CID_ANY) is the wildcard a listener binds to accept
	// connections regardless of the local CID.
	CIDAny uint32 = 0xffffffff
	// CIDHypervisor (VMADDR_CID_HYPERVISOR) addresses services running in
	// the hypervisor (rarely used by guests).
	CIDHypervisor uint32 = 0
	// CIDLocal (VMADDR_CID_LOCAL) is the loopback CID: a socket bound and
	// connected to CIDLocal talks to the same host, when the kernel's
	// vsock-loopback transport is available.
	CIDLocal uint32 = 1
	// CIDHost (VMADDR_CID_HOST) is the well-known CID a guest dials to
	// reach a service on its hypervisor host.
	CIDHost uint32 = 2
)

// afVsock is the AF_VSOCK address family (40 on Linux). It is not exposed
// as a stdlib syscall constant on every Go release, and it is stable
// across kernels, so it is defined here.
const afVsock = 40

// ErrUnsupported is returned by the transport entry points on non-Linux
// platforms, where AF_VSOCK does not exist.
var ErrUnsupported = errors.New("vsock: AF_VSOCK is only supported on Linux")

// Addr is a vsock endpoint. It implements net.Addr; String renders as
// "vsock://<cid>:<port>". The scheme is a human-readable handle, not a
// resolvable URL.
type Addr struct {
	CID  uint32
	Port uint32
}

// Network returns "vsock", satisfying net.Addr.
func (a Addr) Network() string { return "vsock" }

// String renders the endpoint as "vsock://<cid>:<port>".
func (a Addr) String() string { return fmt.Sprintf("vsock://%d:%d", a.CID, a.Port) }

// ParseAddr parses "<cid>:<port>", the address form Dialer.DialContext
// accepts. Both fields are unsigned 32-bit decimals.
func ParseAddr(s string) (Addr, error) {
	host, port, ok := strings.Cut(s, ":")
	if !ok {
		return Addr{}, fmt.Errorf("vsock: address %q missing ':' separator", s)
	}
	cid, err := strconv.ParseUint(host, 10, 32)
	if err != nil {
		return Addr{}, fmt.Errorf("vsock: invalid CID %q: %w", host, err)
	}
	p, err := strconv.ParseUint(port, 10, 32)
	if err != nil {
		return Addr{}, fmt.Errorf("vsock: invalid port %q: %w", port, err)
	}
	return Addr{CID: uint32(cid), Port: uint32(p)}, nil
}

// sockaddrVM is the userspace mirror of struct sockaddr_vm
// (linux/vm_sockets.h). It is 16 bytes: family(2) reserved(2) port(4)
// cid(4) zero(4).
type sockaddrVM struct {
	family   uint16
	reserved uint16
	port     uint32
	cid      uint32
}

// sockaddrVMLen is sizeof(struct sockaddr_vm).
const sockaddrVMLen = 16

// marshal encodes the address in the host's native byte order — the layout
// the kernel reads on the current architecture, including big-endian
// s390x. The trailing four zero bytes are the struct's svm_zero padding.
func (sa sockaddrVM) marshal() []byte {
	b := make([]byte, sockaddrVMLen)
	binary.NativeEndian.PutUint16(b[0:2], sa.family)
	binary.NativeEndian.PutUint16(b[2:4], sa.reserved)
	binary.NativeEndian.PutUint32(b[4:8], sa.port)
	binary.NativeEndian.PutUint32(b[8:12], sa.cid)
	return b
}

// parseSockaddrVM decodes the port and cid a kernel wrote into a
// sockaddr_vm buffer (e.g. the peer address filled in by accept4). Short
// buffers decode the fields that are present and leave the rest zero.
func parseSockaddrVM(b []byte) sockaddrVM {
	var sa sockaddrVM
	if len(b) >= 4 {
		sa.family = binary.NativeEndian.Uint16(b[0:2])
		sa.reserved = binary.NativeEndian.Uint16(b[2:4])
	}
	if len(b) >= 8 {
		sa.port = binary.NativeEndian.Uint32(b[4:8])
	}
	if len(b) >= 12 {
		sa.cid = binary.NativeEndian.Uint32(b[8:12])
	}
	return sa
}

// AllocateCID derives a deterministic, collision-resistant guest CID from
// a (namespace, identifier) pair. The same inputs always map to the same
// CID, so a caller can recompute a VM's CID from its record without
// persisting a counter. An empty identifier returns 0 (unassigned), which
// a caller can treat as "no CID".
//
// The result lands in [CIDFirst, CIDLast], a window chosen to avoid both
// the reserved CIDs (0/1/2 and CIDAny) and the low 3..255 range that
// hypervisors tend to hand-pick for early boot.
func AllocateCID(namespace, identifier string) uint32 {
	if identifier == "" {
		return 0
	}
	h := sha256.Sum256([]byte(namespace + "/" + identifier))
	raw := binary.BigEndian.Uint32(h[:4])
	return CIDFirst + (raw % cidRange)
}

const (
	// CIDFirst is the lowest CID AllocateCID will return: the first entry
	// past the 3..255 range hypervisors commonly reserve.
	CIDFirst uint32 = 0x00010000
	// CIDLast is the highest CID AllocateCID will return: one below CIDAny.
	CIDLast uint32 = 0xfffefffe
	// cidRange is the inclusive size of the allocation window.
	cidRange uint32 = CIDLast - CIDFirst + 1
)

// Dialer opens vsock connections with an optional connect-retry policy.
// The host side of a freshly booted guest is not always ready the instant
// the guest starts dialing; Retries with a short Delay covers that gap
// without a fixed sleep. The zero Dialer performs a single attempt.
type Dialer struct {
	// Retries is the number of additional attempts after the first. A
	// value <= 0 means a single attempt.
	Retries int
	// Delay is slept between attempts.
	Delay time.Duration
}

// Dial connects to (cid, port), retrying per the Dialer's policy.
func (d Dialer) Dial(cid, port uint32) (Conn, error) {
	attempts := d.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(d.Delay)
		}
		c, err := Dial(cid, port)
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// DialContext parses addr as "<cid>:<port>" and dials it, honoring ctx
// cancellation between retries. Its signature matches what
// google.golang.org/grpc's WithContextDialer expects, so a Dialer can be
// handed to gRPC directly.
func (d Dialer) DialContext(ctx context.Context, addr string) (Conn, error) {
	a, err := ParseAddr(addr)
	if err != nil {
		return nil, err
	}
	attempts := d.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(d.Delay):
			}
		}
		c, err := Dial(a.CID, a.Port)
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}
