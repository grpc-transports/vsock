# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to adhere to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [v0.1.0] — 2026-07-07

### Added

- Initial `github.com/grpc-transports/vsock` module — a pure-Go (CGO=0)
  `AF_VSOCK` transport consolidating the vsock dial/listen code previously
  duplicated across two internal repos.
- `Dial`, `Dialer` (with `Dial` / `DialContext` and a connect-retry policy),
  and `Listen` returning a `net.Listener` of gRPC-ready connections whose
  `RemoteAddr` reports the peer CID.
- Reserved-CID constants, `Addr` / `ParseAddr`, `LocalCID`, `Supported`, and
  deterministic `AllocateCID`.
- Linux implementation behind `//go:build linux` with a portable `!linux`
  stub returning `ErrUnsupported`, so `go build` / `go vet` stay green on
  every GOOS.
- Six-arch CI (amd64, arm64, riscv64, loong64, ppc64le, big-endian s390x)
  behind a 100% statement-coverage gate; the syscall layer is driven through
  injectable seams, so no vsock device or root is required.
