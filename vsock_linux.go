//go:build linux

package vsock

import (
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// Syscall seams. Every kernel entry point the transport needs is reached
// through a package variable so tests can drive every success and error
// branch deterministically, without a vsock-capable host or root. The real
// implementations are thin wrappers over syscall; tests swap them for
// in-memory fakes.
var (
	sysSocket = func(domain, typ, proto int) (int, error) {
		return syscall.Socket(domain, typ, proto)
	}
	sysBind    = rawBind
	sysConnect = rawConnect
	sysListen  = func(fd, backlog int) error { return syscall.Listen(fd, backlog) }
	sysAccept  = rawAccept
	sysClose   = func(fd int) error { return syscall.Close(fd) }
	sysOpen    = func(path string, mode int, perm uint32) (int, error) {
		return syscall.Open(path, mode, perm)
	}
	sysLocalCID = rawLocalCID

	// fdToConn turns a connected vsock file descriptor into a net.Conn.
	// net.FileConn dups the fd, so the original is closed here. It is a
	// seam so tests need no real socket.
	fdToConn = func(fd int, name string) (net.Conn, error) {
		f := os.NewFile(uintptr(fd), name)
		c, err := net.FileConn(f)
		_ = f.Close()
		return c, err
	}
)

// errnoErr converts a raw syscall errno into an error, returning nil for
// the success value 0. Isolating it here keeps the raw* wrappers to a
// single, always-executed statement, so their coverage does not depend on
// owning a vsock device to reach a success return.
func errnoErr(errno syscall.Errno) error {
	if errno == 0 {
		return nil
	}
	return errno
}

// rawBind binds fd to the marshalled sockaddr_vm in sa.
func rawBind(fd int, sa []byte) error {
	_, _, errno := syscall.RawSyscall(
		syscall.SYS_BIND, uintptr(fd),
		uintptr(unsafe.Pointer(&sa[0])), uintptr(len(sa)),
	)
	return errnoErr(errno)
}

// rawConnect connects fd to the marshalled sockaddr_vm in sa.
func rawConnect(fd int, sa []byte) error {
	_, _, errno := syscall.RawSyscall(
		syscall.SYS_CONNECT, uintptr(fd),
		uintptr(unsafe.Pointer(&sa[0])), uintptr(len(sa)),
	)
	return errnoErr(errno)
}

// rawAccept accepts one connection on fd via accept4 (SYS_ACCEPT is absent
// on s390x, so accept4 with flags 0 is used for portability across all six
// 64-bit arches). It returns the new fd and the raw peer sockaddr_vm bytes;
// on error the fd/bytes are ignored by the caller.
func rawAccept(fd int) (int, []byte, error) {
	buf := make([]byte, sockaddrVMLen)
	salen := uint32(len(buf))
	nfd, _, errno := syscall.Syscall6(
		syscall.SYS_ACCEPT4, uintptr(fd),
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&salen)),
		0, 0, 0,
	)
	return int(nfd), buf, errnoErr(errno)
}

// ioctlGetLocalCID is IOCTL_VM_SOCKETS_GET_LOCAL_CID (_IO(7, 0xb9)); the
// value is identical on every arch the transport targets.
const ioctlGetLocalCID = 0x7b9

// rawLocalCID reads the calling context's guest CID through an ioctl on an
// already-open /dev/vsock fd.
func rawLocalCID(fd int) (uint32, error) {
	var cid uint32
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL, uintptr(fd),
		uintptr(ioctlGetLocalCID),
		uintptr(unsafe.Pointer(&cid)),
	)
	return cid, errnoErr(errno)
}

// Dial opens an AF_VSOCK stream connection to (cid, port) and returns it as
// a net.Conn. Dialing CIDHost from inside a guest reaches a service on the
// hypervisor.
func Dial(cid, port uint32) (Conn, error) {
	fd, err := sysSocket(afVsock, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock: socket: %w", err)
	}
	sa := sockaddrVM{family: afVsock, port: port, cid: cid}
	if err := sysConnect(fd, sa.marshal()); err != nil {
		_ = sysClose(fd)
		return nil, fmt.Errorf("vsock: connect (%d:%d): %w", cid, port, err)
	}
	conn, err := fdToConn(fd, Addr{CID: cid, Port: port}.String())
	if err != nil {
		return nil, fmt.Errorf("vsock: fileconn: %w", err)
	}
	return conn, nil
}

// Listen binds an AF_VSOCK listener on (CIDAny, port) and returns a
// net.Listener whose connections are ready for grpc.Server.Serve. A port
// of 0 lets the kernel choose an ephemeral one.
func Listen(port uint32) (net.Listener, error) {
	fd, err := sysSocket(afVsock, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock: socket: %w", err)
	}
	sa := sockaddrVM{family: afVsock, port: port, cid: CIDAny}
	if err := sysBind(fd, sa.marshal()); err != nil {
		_ = sysClose(fd)
		return nil, fmt.Errorf("vsock: bind (port=%d): %w", port, err)
	}
	if err := sysListen(fd, 64); err != nil {
		_ = sysClose(fd)
		return nil, fmt.Errorf("vsock: listen: %w", err)
	}
	return &listener{fd: fd, port: port}, nil
}

// listener implements net.Listener over a bound AF_VSOCK socket.
type listener struct {
	fd     int
	port   uint32
	closed atomic.Bool
}

// Accept blocks for the next guest connection and returns it wrapped so
// RemoteAddr reports the peer's Addr (its CID and port).
func (l *listener) Accept() (net.Conn, error) {
	if l.closed.Load() {
		return nil, net.ErrClosed
	}
	nfd, sab, err := sysAccept(l.fd)
	if err != nil {
		if l.closed.Load() {
			return nil, net.ErrClosed
		}
		return nil, fmt.Errorf("vsock: accept: %w", err)
	}
	peer := parseSockaddrVM(sab)
	c, err := fdToConn(nfd, Addr{CID: peer.cid, Port: peer.port}.String())
	if err != nil {
		return nil, fmt.Errorf("vsock: accept fileconn: %w", err)
	}
	return &vsockConn{Conn: c, remote: Addr{CID: peer.cid, Port: peer.port}, local: Addr{CID: CIDAny, Port: l.port}}, nil
}

// Close stops the listener. It is idempotent and safe under a concurrent
// Accept, which observes the close and returns net.ErrClosed.
func (l *listener) Close() error {
	if !l.closed.CompareAndSwap(false, true) {
		return nil
	}
	return sysClose(l.fd)
}

// Addr returns the listener's local Addr (CIDAny and the bound port).
func (l *listener) Addr() net.Addr { return Addr{CID: CIDAny, Port: l.port} }

// vsockConn wraps an accepted connection so its RemoteAddr and LocalAddr
// report vsock Addrs, letting gRPC handlers read the peer CID through
// peer.FromContext.
type vsockConn struct {
	net.Conn
	remote Addr
	local  Addr
}

// RemoteAddr returns the peer's vsock Addr.
func (c *vsockConn) RemoteAddr() net.Addr { return c.remote }

// LocalAddr returns the listener's vsock Addr.
func (c *vsockConn) LocalAddr() net.Addr { return c.local }

// LocalCID reports the calling context's own AF_VSOCK guest CID, read from
// the kernel via ioctl on /dev/vsock. It returns 0 when the CID cannot be
// determined (no /dev/vsock, ioctl failure, or a non-guest host), which a
// caller can treat as "unknown".
func LocalCID() uint32 {
	fd, err := sysOpen("/dev/vsock", syscall.O_RDONLY, 0)
	if err != nil {
		return 0
	}
	defer func() { _ = sysClose(fd) }()
	cid, err := sysLocalCID(fd)
	if err != nil {
		return 0
	}
	return cid
}

// Supported reports whether the running kernel exposes AF_VSOCK, probed by
// opening and immediately closing a socket. It is false on a Linux host
// without the vsock module loaded.
func Supported() bool {
	fd, err := sysSocket(afVsock, syscall.SOCK_STREAM, 0)
	if err != nil {
		return false
	}
	_ = sysClose(fd)
	return true
}
