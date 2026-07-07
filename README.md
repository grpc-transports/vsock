<p align="center"><img src="https://raw.githubusercontent.com/grpc-transports/brand/main/social/grpc-transports.png" alt="grpc-transports/vsock" width="720"></p>

# vsock

Pure-Go (CGO=0) `AF_VSOCK` transport for gRPC: a `net.Conn` dialer and a `net.Listener` over the Linux virtio-vsock address family, with **no third-party dependencies**.

`AF_VSOCK` is the host↔guest socket family used by KVM/QEMU virtio-vsock and Apple Virtualization (`VZVirtioSocketDevice`). Both expose the same kernel interface to a Linux guest, so one implementation serves either hypervisor. An address is a `(context id, port)` pair — the context id ("CID") names a VM, the port names a service inside it.

Built to carry gRPC between a hypervisor host and its guest microVMs — the server's `Listen` returns connections you hand straight to `grpc.Server.Serve`, and `Dialer.DialContext` has the signature `grpc.WithContextDialer` expects — but nothing here depends on gRPC; the surface is plain `net.Conn` / `net.Listener`.

For gRPC across hosts and zones see the sibling [`wireguard`](https://github.com/grpc-transports/wireguard); for human-driven clients see [`ssh`](https://github.com/grpc-transports/ssh).

## Module

```
github.com/grpc-transports/vsock
```

## API

### Reserved CIDs

```go
const (
    CIDAny        uint32 = 0xffffffff // VMADDR_CID_ANY   — listener wildcard
    CIDHypervisor uint32 = 0          // VMADDR_CID_HYPERVISOR
    CIDLocal      uint32 = 1          // VMADDR_CID_LOCAL — loopback
    CIDHost       uint32 = 2          // VMADDR_CID_HOST  — a guest dials this to reach its host
)
```

### Server

```go
// Listen binds an AF_VSOCK listener on (CIDAny, port) and returns a
// net.Listener whose connections are ready for grpc.Server.Serve.
// port 0 lets the kernel choose an ephemeral port.
func Listen(port uint32) (net.Listener, error)
```

Accepted connections report the peer's `Addr` from `RemoteAddr()`, so gRPC handlers can read the caller's CID via `peer.FromContext`.

### Client

```go
// Dial opens an AF_VSOCK stream connection to (cid, port).
func Dial(cid, port uint32) (net.Conn, error)

// Dialer adds an optional connect-retry policy; the zero Dialer does one attempt.
type Dialer struct {
    Retries int           // additional attempts after the first
    Delay   time.Duration // slept between attempts
}
func (d Dialer) Dial(cid, port uint32) (net.Conn, error)
func (d Dialer) DialContext(ctx context.Context, addr string) (net.Conn, error) // addr = "<cid>:<port>"
```

### Helpers

```go
// LocalCID reports the calling context's own guest CID (0 if unknown).
func LocalCID() uint32

// Supported reports whether the running kernel exposes AF_VSOCK.
func Supported() bool

// AllocateCID derives a deterministic, collision-resistant guest CID in the
// non-reserved window [CIDFirst, CIDLast] from a (namespace, identifier) pair.
func AllocateCID(namespace, identifier string) uint32
```

## Usage

**Host side (agent) — listen for guests:**

```go
lis, err := vsock.Listen(5555)
if err != nil {
    log.Fatal(err)
}
grpcServer.Serve(lis)
```

**Guest side — dial the host, with a short boot-race retry:**

```go
opt := grpc.WithContextDialer((vsock.Dialer{Retries: 10, Delay: 100 * time.Millisecond}).DialContext)
conn, err := grpc.NewClient(fmt.Sprintf("%d:%d", vsock.CIDHost, 5555), opt, grpc.WithTransportCredentials(insecure.NewCredentials()))
```

## Platform support

The transport is **Linux-only**. On every other GOOS the package still builds and `go vet`s: `Dial`, `Listen`, `LocalCID` and `Supported` are present but return `ErrUnsupported` (or a zero value), so cross-platform callers compile without a build-tag fork of their own. The pure CID, address, and `sockaddr_vm` logic runs everywhere.

CI runs the suite on all six 64-bit Go arches — amd64, arm64, riscv64, loong64, ppc64le, and **big-endian s390x** (a genuine endianness check of the native-order `sockaddr_vm` marshalling) — behind a hard **100% statement-coverage gate**, error branches included. No vsock device is needed: the syscall layer is reached through package seams the tests swap for in-memory fakes.

## License

BSD-3-Clause. See [LICENSE](LICENSE).
