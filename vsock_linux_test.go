//go:build linux

package vsock

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

// errFake is a sentinel used to distinguish injected failures.
var errFake = errors.New("injected failure")

// seams snapshots and restores every syscall seam so each test can install
// its own fakes and roll back cleanly.
type seams struct {
	socket   func(domain, typ, proto int) (int, error)
	bind     func(fd int, sa []byte) error
	connect  func(fd int, sa []byte) error
	listen   func(fd, backlog int) error
	accept   func(fd int) (int, []byte, error)
	closeFn  func(fd int) error
	open     func(path string, mode int, perm uint32) (int, error)
	localCID func(fd int) (uint32, error)
	toConn   func(fd int, name string) (net.Conn, error)
}

func save() seams {
	return seams{sysSocket, sysBind, sysConnect, sysListen, sysAccept, sysClose, sysOpen, sysLocalCID, fdToConn}
}

func (s seams) restore() {
	sysSocket, sysBind, sysConnect, sysListen, sysAccept, sysClose, sysOpen, sysLocalCID, fdToConn =
		s.socket, s.bind, s.connect, s.listen, s.accept, s.closeFn, s.open, s.localCID, s.toConn
}

// pipeConn returns one end of a net.Pipe as a stand-in for a real accepted
// vsock connection, closing the other end when the test is done.
func pipeConn(t *testing.T) net.Conn {
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return a
}

func TestDial(t *testing.T) {
	t.Run("socket error", func(t *testing.T) {
		defer save().restore()
		sysSocket = func(int, int, int) (int, error) { return -1, errFake }
		if _, err := Dial(CIDHost, 1); !errors.Is(err, errFake) {
			t.Fatalf("err = %v, want errFake", err)
		}
	})

	t.Run("connect error closes fd", func(t *testing.T) {
		defer save().restore()
		var closed int
		sysSocket = func(int, int, int) (int, error) { return 7, nil }
		sysConnect = func(int, []byte) error { return errFake }
		sysClose = func(fd int) error { closed = fd; return nil }
		if _, err := Dial(CIDHost, 1); !errors.Is(err, errFake) {
			t.Fatalf("err = %v, want errFake", err)
		}
		if closed != 7 {
			t.Errorf("fd %d closed, want 7", closed)
		}
	})

	t.Run("fileconn error", func(t *testing.T) {
		defer save().restore()
		sysSocket = func(int, int, int) (int, error) { return 7, nil }
		sysConnect = func(int, []byte) error { return nil }
		fdToConn = func(int, string) (net.Conn, error) { return nil, errFake }
		if _, err := Dial(CIDHost, 1); !errors.Is(err, errFake) {
			t.Fatalf("err = %v, want errFake", err)
		}
	})

	t.Run("success", func(t *testing.T) {
		defer save().restore()
		sysSocket = func(int, int, int) (int, error) { return 7, nil }
		sysConnect = func(int, []byte) error { return nil }
		fdToConn = func(int, string) (net.Conn, error) { return pipeConn(t), nil }
		c, err := Dial(CIDHost, 1)
		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
		_ = c.Close()
	})
}

func TestListen(t *testing.T) {
	t.Run("socket error", func(t *testing.T) {
		defer save().restore()
		sysSocket = func(int, int, int) (int, error) { return -1, errFake }
		if _, err := Listen(1); !errors.Is(err, errFake) {
			t.Fatalf("err = %v, want errFake", err)
		}
	})

	t.Run("bind error closes fd", func(t *testing.T) {
		defer save().restore()
		var closed int
		sysSocket = func(int, int, int) (int, error) { return 9, nil }
		sysBind = func(int, []byte) error { return errFake }
		sysClose = func(fd int) error { closed = fd; return nil }
		if _, err := Listen(1); !errors.Is(err, errFake) {
			t.Fatalf("err = %v, want errFake", err)
		}
		if closed != 9 {
			t.Errorf("fd %d closed, want 9", closed)
		}
	})

	t.Run("listen error closes fd", func(t *testing.T) {
		defer save().restore()
		var closed int
		sysSocket = func(int, int, int) (int, error) { return 9, nil }
		sysBind = func(int, []byte) error { return nil }
		sysListen = func(int, int) error { return errFake }
		sysClose = func(fd int) error { closed = fd; return nil }
		if _, err := Listen(1); !errors.Is(err, errFake) {
			t.Fatalf("err = %v, want errFake", err)
		}
		if closed != 9 {
			t.Errorf("fd %d closed, want 9", closed)
		}
	})

	t.Run("success and Addr", func(t *testing.T) {
		defer save().restore()
		sysSocket = func(int, int, int) (int, error) { return 9, nil }
		sysBind = func(int, []byte) error { return nil }
		sysListen = func(int, int) error { return nil }
		sysClose = func(int) error { return nil }
		l, err := Listen(4242)
		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
		want := Addr{CID: CIDAny, Port: 4242}
		if l.Addr() != want {
			t.Errorf("Addr() = %v, want %v", l.Addr(), want)
		}
		if err := l.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
}

func newTestListener(fd int, port uint32) *listener { return &listener{fd: fd, port: port} }

func TestAccept(t *testing.T) {
	t.Run("closed before accept", func(t *testing.T) {
		l := newTestListener(3, 1)
		l.closed.Store(true)
		if _, err := l.Accept(); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("err = %v, want ErrClosed", err)
		}
	})

	t.Run("accept error while open", func(t *testing.T) {
		defer save().restore()
		sysAccept = func(int) (int, []byte, error) { return 0, nil, errFake }
		l := newTestListener(3, 1)
		if _, err := l.Accept(); !errors.Is(err, errFake) {
			t.Fatalf("err = %v, want errFake", err)
		}
	})

	t.Run("accept error after close reports ErrClosed", func(t *testing.T) {
		defer save().restore()
		sysClose = func(int) error { return nil }
		l := newTestListener(3, 1)
		sysAccept = func(int) (int, []byte, error) {
			_ = l.Close() // simulate Close racing with a blocked Accept
			return 0, nil, errFake
		}
		if _, err := l.Accept(); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("err = %v, want ErrClosed", err)
		}
	})

	t.Run("fileconn error", func(t *testing.T) {
		defer save().restore()
		sa := sockaddrVM{cid: 42, port: 7}.marshal()
		sysAccept = func(int) (int, []byte, error) { return 11, sa, nil }
		fdToConn = func(int, string) (net.Conn, error) { return nil, errFake }
		l := newTestListener(3, 1)
		if _, err := l.Accept(); !errors.Is(err, errFake) {
			t.Fatalf("err = %v, want errFake", err)
		}
	})

	t.Run("success wraps peer addr", func(t *testing.T) {
		defer save().restore()
		sa := sockaddrVM{cid: 42, port: 7}.marshal()
		sysAccept = func(int) (int, []byte, error) { return 11, sa, nil }
		fdToConn = func(int, string) (net.Conn, error) { return pipeConn(t), nil }
		l := newTestListener(3, 9000)
		c, err := l.Accept()
		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
		if got := c.RemoteAddr(); got != (Addr{CID: 42, Port: 7}) {
			t.Errorf("RemoteAddr = %v, want vsock 42:7", got)
		}
		if got := c.LocalAddr(); got != (Addr{CID: CIDAny, Port: 9000}) {
			t.Errorf("LocalAddr = %v, want CIDAny:9000", got)
		}
		_ = c.Close()
	})
}

func TestListenerCloseIdempotent(t *testing.T) {
	defer save().restore()
	var calls int
	sysClose = func(int) error { calls++; return nil }
	l := newTestListener(5, 1)
	if err := l.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if calls != 1 {
		t.Errorf("sysClose called %d times, want 1", calls)
	}
}

func TestLocalCID(t *testing.T) {
	t.Run("open error", func(t *testing.T) {
		defer save().restore()
		sysOpen = func(string, int, uint32) (int, error) { return -1, errFake }
		if got := LocalCID(); got != 0 {
			t.Errorf("LocalCID = %d, want 0", got)
		}
	})

	t.Run("ioctl error", func(t *testing.T) {
		defer save().restore()
		sysOpen = func(string, int, uint32) (int, error) { return 8, nil }
		sysClose = func(int) error { return nil }
		sysLocalCID = func(int) (uint32, error) { return 0, errFake }
		if got := LocalCID(); got != 0 {
			t.Errorf("LocalCID = %d, want 0", got)
		}
	})

	t.Run("success", func(t *testing.T) {
		defer save().restore()
		sysOpen = func(string, int, uint32) (int, error) { return 8, nil }
		sysClose = func(int) error { return nil }
		sysLocalCID = func(int) (uint32, error) { return 12345, nil }
		if got := LocalCID(); got != 12345 {
			t.Errorf("LocalCID = %d, want 12345", got)
		}
	})
}

func TestSupported(t *testing.T) {
	t.Run("unsupported", func(t *testing.T) {
		defer save().restore()
		sysSocket = func(int, int, int) (int, error) { return -1, errFake }
		if Supported() {
			t.Error("Supported = true, want false")
		}
	})

	t.Run("supported", func(t *testing.T) {
		defer save().restore()
		sysSocket = func(int, int, int) (int, error) { return 3, nil }
		sysClose = func(int) error { return nil }
		if !Supported() {
			t.Error("Supported = false, want true")
		}
	})
}

func TestDialer(t *testing.T) {
	// Program Dial's outcome through the seams: succeed only on the Nth
	// connect attempt.
	program := func(t *testing.T, failFirst int) {
		var n int
		sysSocket = func(int, int, int) (int, error) { return 7, nil }
		sysClose = func(int) error { return nil }
		sysConnect = func(int, []byte) error {
			n++
			if n <= failFirst {
				return errFake
			}
			return nil
		}
		fdToConn = func(int, string) (net.Conn, error) { return pipeConn(t), nil }
	}

	t.Run("retry then success", func(t *testing.T) {
		defer save().restore()
		program(t, 1)
		c, err := Dialer{Retries: 3}.Dial(CIDHost, 1)
		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
		_ = c.Close()
	})

	t.Run("all attempts fail", func(t *testing.T) {
		defer save().restore()
		program(t, 99)
		if _, err := (Dialer{Retries: 2}).Dial(CIDHost, 1); !errors.Is(err, errFake) {
			t.Fatalf("err = %v, want errFake", err)
		}
	})

	t.Run("negative retries clamps to one attempt", func(t *testing.T) {
		defer save().restore()
		program(t, 99)
		if _, err := (Dialer{Retries: -5}).Dial(CIDHost, 1); !errors.Is(err, errFake) {
			t.Fatalf("err = %v, want errFake", err)
		}
	})
}

func TestDialerContext(t *testing.T) {
	t.Run("bad address", func(t *testing.T) {
		if _, err := (Dialer{}).DialContext(context.Background(), "nope"); err == nil {
			t.Fatal("want parse error")
		}
	})

	t.Run("success first try", func(t *testing.T) {
		defer save().restore()
		sysSocket = func(int, int, int) (int, error) { return 7, nil }
		sysConnect = func(int, []byte) error { return nil }
		fdToConn = func(int, string) (net.Conn, error) { return pipeConn(t), nil }
		c, err := (Dialer{}).DialContext(context.Background(), "2:5555")
		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
		_ = c.Close()
	})

	t.Run("retry then success", func(t *testing.T) {
		defer save().restore()
		var n int
		sysSocket = func(int, int, int) (int, error) { return 7, nil }
		sysClose = func(int) error { return nil }
		sysConnect = func(int, []byte) error {
			n++
			if n == 1 {
				return errFake
			}
			return nil
		}
		fdToConn = func(int, string) (net.Conn, error) { return pipeConn(t), nil }
		c, err := (Dialer{Retries: 2, Delay: time.Millisecond}).DialContext(context.Background(), "2:5555")
		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
		_ = c.Close()
	})

	t.Run("all fail returns last error", func(t *testing.T) {
		defer save().restore()
		sysSocket = func(int, int, int) (int, error) { return 7, nil }
		sysClose = func(int) error { return nil }
		sysConnect = func(int, []byte) error { return errFake }
		if _, err := (Dialer{Retries: 1, Delay: time.Millisecond}).DialContext(context.Background(), "2:5555"); !errors.Is(err, errFake) {
			t.Fatalf("err = %v, want errFake", err)
		}
	})

	t.Run("negative retries clamps to one attempt", func(t *testing.T) {
		defer save().restore()
		sysSocket = func(int, int, int) (int, error) { return 7, nil }
		sysClose = func(int) error { return nil }
		sysConnect = func(int, []byte) error { return errFake }
		if _, err := (Dialer{Retries: -5}).DialContext(context.Background(), "2:5555"); !errors.Is(err, errFake) {
			t.Fatalf("err = %v, want errFake", err)
		}
	})

	t.Run("context cancelled between attempts", func(t *testing.T) {
		defer save().restore()
		sysSocket = func(int, int, int) (int, error) { return 7, nil }
		sysClose = func(int) error { return nil }
		sysConnect = func(int, []byte) error { return errFake }
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // already cancelled; the long Delay never fires
		_, err := (Dialer{Retries: 3, Delay: time.Hour}).DialContext(ctx, "2:5555")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
}

// TestRealSeams exercises the default (non-faked) syscall wrappers so their
// bodies are covered without a vsock device: error paths run against an
// invalid fd, and fdToConn/sysClose/sysOpen run against a real socketpair
// and /dev/null. This needs neither a vsock-capable host nor root.
func TestErrnoErr(t *testing.T) {
	if err := errnoErr(0); err != nil {
		t.Errorf("errnoErr(0) = %v, want nil", err)
	}
	if err := errnoErr(syscall.EBADF); !errors.Is(err, syscall.EBADF) {
		t.Errorf("errnoErr(EBADF) = %v, want EBADF", err)
	}
}

func TestRealSeams(t *testing.T) {
	// Invalid-fd error paths through the real raw* wrappers.
	if err := rawBind(-1, make([]byte, sockaddrVMLen)); err == nil {
		t.Error("rawBind(-1): want error")
	}
	if err := rawConnect(-1, make([]byte, sockaddrVMLen)); err == nil {
		t.Error("rawConnect(-1): want error")
	}
	if _, _, err := rawAccept(-1); err == nil {
		t.Error("rawAccept(-1): want error")
	}
	if _, err := rawLocalCID(-1); err == nil {
		t.Error("rawLocalCID(-1): want error")
	}

	// Real fdToConn against a genuine socket (socketpair) — covers the
	// success return; net.FileConn dups the fd, so close both ends.
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	defer func() { _ = sysClose(fds[1]) }()
	c, err := fdToConn(fds[0], "vsock://test")
	if err != nil {
		t.Fatalf("fdToConn on real socket: %v", err)
	}
	_ = c.Close()

	// Real sysListen on an invalid fd — covers the wrapper's error return.
	if err := sysListen(-1, 1); err == nil {
		t.Error("sysListen(-1): want error")
	}

	// Real sysOpen + sysClose against a file that always exists.
	fd, err := sysOpen(os.DevNull, syscall.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("sysOpen(%s): %v", os.DevNull, err)
	}
	if err := sysClose(fd); err != nil {
		t.Errorf("sysClose: %v", err)
	}

	// Real sysSocket for AF_VSOCK: on a vsock-capable host it succeeds
	// (close it); otherwise it errors. Either way the wrapper body runs.
	if vfd, err := sysSocket(afVsock, syscall.SOCK_STREAM, 0); err == nil {
		_ = sysClose(vfd)
	}
}

// TestRealLoopback optionally drives a genuine end-to-end connection over
// the kernel's vsock loopback transport (VMADDR_CID_LOCAL). It is skipped
// cleanly whenever the running kernel lacks vsock-loopback — which is the
// case under QEMU and on hosts without the module — so it never fails CI.
func TestRealLoopback(t *testing.T) {
	if !Supported() {
		t.Skip("AF_VSOCK not available on this host")
	}
	l, err := Listen(0)
	if err != nil {
		t.Skipf("vsock listen unavailable: %v", err)
	}
	defer func() { _ = l.Close() }()
	port := l.Addr().(Addr).Port

	done := make(chan error, 1)
	go func() {
		c, aerr := l.Accept()
		if aerr != nil {
			done <- aerr
			return
		}
		defer c.Close()
		buf := make([]byte, 4)
		_, rerr := c.Read(buf)
		done <- rerr
	}()

	c, err := Dial(CIDLocal, port)
	if err != nil {
		t.Skipf("vsock loopback dial unavailable: %v", err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("accept/read: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("loopback round-trip timed out")
	}
}
