//go:build !linux

package vsock

import (
	"context"
	"errors"
	"testing"
)

// On non-Linux platforms the transport entry points are stubs. These tests
// assert the stub contract and keep `go test ./...` green on macOS/Windows.

func TestStubDial(t *testing.T) {
	if _, err := Dial(CIDHost, 5555); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Dial err = %v, want ErrUnsupported", err)
	}
}

func TestStubListen(t *testing.T) {
	if _, err := Listen(5555); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Listen err = %v, want ErrUnsupported", err)
	}
}

func TestStubLocalCID(t *testing.T) {
	if got := LocalCID(); got != 0 {
		t.Errorf("LocalCID() = %d, want 0", got)
	}
}

func TestStubSupported(t *testing.T) {
	if Supported() {
		t.Error("Supported() = true, want false on non-Linux")
	}
}

// The portable Dialer routes through Dial, so on a stub platform it
// surfaces ErrUnsupported after exhausting its attempts.
func TestStubDialer(t *testing.T) {
	d := Dialer{Retries: 1}
	if _, err := d.Dial(CIDHost, 5555); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Dialer.Dial err = %v, want ErrUnsupported", err)
	}
	if _, err := d.DialContext(context.Background(), "2:5555"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Dialer.DialContext err = %v, want ErrUnsupported", err)
	}
	if _, err := d.DialContext(context.Background(), "bad"); err == nil {
		t.Error("Dialer.DialContext(bad addr): want error")
	}
}
