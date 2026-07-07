package vsock

import (
	"encoding/binary"
	"errors"
	"testing"
)

func TestAddr(t *testing.T) {
	a := Addr{CID: CIDHost, Port: 5555}
	if got := a.Network(); got != "vsock" {
		t.Errorf("Network() = %q, want vsock", got)
	}
	if got := a.String(); got != "vsock://2:5555" {
		t.Errorf("String() = %q, want vsock://2:5555", got)
	}
}

func TestReservedCIDs(t *testing.T) {
	// Guard the wire-level constants against accidental edits.
	cases := map[string]struct {
		got, want uint32
	}{
		"CIDAny":        {CIDAny, 0xffffffff},
		"CIDHypervisor": {CIDHypervisor, 0},
		"CIDLocal":      {CIDLocal, 1},
		"CIDHost":       {CIDHost, 2},
	}
	for name, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", name, c.got, c.want)
		}
	}
}

func TestParseAddr(t *testing.T) {
	ok, err := ParseAddr("2:5555")
	if err != nil {
		t.Fatalf("ParseAddr valid: unexpected error %v", err)
	}
	if ok != (Addr{CID: 2, Port: 5555}) {
		t.Errorf("ParseAddr = %v, want {2 5555}", ok)
	}

	for name, in := range map[string]string{
		"no separator": "25555",
		"bad cid":      "x:5555",
		"bad port":     "2:y",
		"cid overflow": "4294967296:1",
		"port empty":   "2:",
	} {
		if _, err := ParseAddr(in); err == nil {
			t.Errorf("ParseAddr(%q) [%s]: want error, got nil", in, name)
		}
	}
}

func TestSockaddrVMMarshalRoundTrip(t *testing.T) {
	sa := sockaddrVM{family: afVsock, reserved: 0, port: 5555, cid: CIDHost}
	b := sa.marshal()
	if len(b) != sockaddrVMLen {
		t.Fatalf("marshal len = %d, want %d", len(b), sockaddrVMLen)
	}
	// The trailing svm_zero padding must stay zero.
	for i := 12; i < 16; i++ {
		if b[i] != 0 {
			t.Errorf("padding byte %d = %d, want 0", i, b[i])
		}
	}
	// Native-endian fields decode back to the originals.
	if fam := binary.NativeEndian.Uint16(b[0:2]); fam != afVsock {
		t.Errorf("family = %d, want %d", fam, afVsock)
	}
	got := parseSockaddrVM(b)
	if got != sa {
		t.Errorf("parseSockaddrVM = %+v, want %+v", got, sa)
	}
}

func TestParseSockaddrVMShort(t *testing.T) {
	// Buffers shorter than a full sockaddr_vm decode only the fields that
	// are present and leave the rest zero — exercises each length guard.
	if got := parseSockaddrVM(nil); got != (sockaddrVM{}) {
		t.Errorf("empty buffer decoded to %+v, want zero", got)
	}
	if got := parseSockaddrVM(make([]byte, 4)); got.port != 0 || got.cid != 0 {
		t.Errorf("4-byte buffer: port/cid should be zero, got %+v", got)
	}
	if got := parseSockaddrVM(make([]byte, 8)); got.cid != 0 {
		t.Errorf("8-byte buffer: cid should be zero, got %+v", got)
	}
}

func TestAllocateCID(t *testing.T) {
	if got := AllocateCID("proj", ""); got != 0 {
		t.Errorf("AllocateCID(_, \"\") = %d, want 0", got)
	}
	// Deterministic.
	first := AllocateCID("proj", "vm-1")
	for i := 0; i < 8; i++ {
		if got := AllocateCID("proj", "vm-1"); got != first {
			t.Fatalf("AllocateCID drift: %d vs %d", got, first)
		}
	}
	// Always in the usable, non-reserved window.
	for _, id := range []string{"a", "vm-1", "vm-2", "zzzzzzzzzzzzzzz"} {
		cid := AllocateCID("ns", id)
		if cid < CIDFirst || cid > CIDLast {
			t.Errorf("AllocateCID(ns,%q) = %d, out of [%d,%d]", id, cid, CIDFirst, CIDLast)
		}
		if cid == CIDAny || cid == CIDHost || cid == CIDLocal || cid == CIDHypervisor {
			t.Errorf("AllocateCID(ns,%q) = %d hit a reserved CID", id, cid)
		}
	}
}

func TestErrUnsupportedIsError(t *testing.T) {
	if !errors.Is(ErrUnsupported, ErrUnsupported) {
		t.Fatal("ErrUnsupported must be a comparable sentinel")
	}
}
