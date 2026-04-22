# udp2tcp-go

High-performance, multi-core UDP ↔ TCP proxy written in Go, purpose-built for WireGuard tunnelling over TCP.  
A direct Go port of [udp2tcp](https://github.com/danny30au/udp2tcp) (Rust).

## Features

- **Multi-core scaling** — `SO_REUSEPORT` per-worker sockets + `runtime.LockOSThread()` for OS-thread affinity; the kernel distributes datagrams across workers (software RSS)
- **Multi-stream TCP** — optional `-tcp-streams N` opens N parallel TCP connections per UDP session and stripes packets round-robin across them, breaking the single-flow congestion-control / single-CPU-queue bottleneck
- **WireGuard-compatible framing** — 2-byte big-endian length prefix (identical to wstunnel / wg-tcp-tunnel wire format)
- **Bidirectional** — UDP→TCP (client side) or TCP→UDP (`-reverse`, server side)
- **Sharded session table** — 256-shard striped RWMutex map; no global lock on the hot path
- **Buffered TCP writes** — `bufio.Writer` merges header+payload into a single `write()` syscall for every normal WireGuard packet
- **Platform-split socket code** — `golang.org/x/sys/unix` on Linux (correct musl/glibc constants); graceful fallback on other OSes
- **Zero CGO** — fully static binary, no libc dependency
- **OpenWrt-ready** — cross-compiles for x86-64, arm64, armv7, mipsle, mips, mips64; includes package Makefile + init.d + UCI config

## Pre-built binaries

Download from [GitHub Releases](https://github.com/danny30au/udp2tcp-go/releases):

| Target | File | Notes |
|--------|------|-------|
| OpenWrt x86-64 | `udp2tcp-linux-x86_64` | `GOAMD64=v2` baseline (SSE2+, all x86-64 OpenWrt routers) |
| OpenWrt x86-64 modern | `udp2tcp-linux-x86_64_v3` | `GOAMD64=v3` (AVX2+, faster on newer x86 hardware) |
| OpenWrt arm64 | `udp2tcp-linux-arm64` | RPi 4/5, NanoPi R5S, etc. |
| OpenWrt armv7 | `udp2tcp-linux-armv7` | Most ARM OpenWrt routers |
| OpenWrt mipsle softfloat | `udp2tcp-linux-mipsle-softfloat` | MT7621, MT7628 (Archer C7, GL-MT300N, etc.) |
| OpenWrt mips softfloat | `udp2tcp-linux-mips-softfloat` | Atheros AR71xx/AR9xxx |
| OpenWrt mips64 | `udp2tcp-linux-mips64` | Octeon |

## Build

```bash
# Local host binary
go build -ldflags="-s -w" -trimpath -o udp2tcp ./cmd/udp2tcp

# OpenWrt x86-64 (GOAMD64=v2 — correct baseline for all OpenWrt x86-64 targets)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v2 \
  go build -ldflags="-s -w" -trimpath -o udp2tcp-x86_64 ./cmd/udp2tcp

# OpenWrt arm64
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -ldflags="-s -w" -trimpath -o udp2tcp-arm64 ./cmd/udp2tcp

# OpenWrt armv7
CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 \
  go build -ldflags="-s -w" -trimpath -o udp2tcp-armv7 ./cmd/udp2tcp

# OpenWrt mipsle softfloat (MT7621 / MT7628 — most cheap routers)
CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat \
  go build -ldflags="-s -w" -trimpath -o udp2tcp-mipsle ./cmd/udp2tcp

# OpenWrt mips softfloat (Atheros AR71xx)
CGO_ENABLED=0 GOOS=linux GOARCH=mips GOMIPS=softfloat \
  go build -ldflags="-s -w" -trimpath -o udp2tcp-mips ./cmd/udp2tcp
```

## OpenWrt SDK build (package feed)

```bash
# 1. Copy the package Makefile into your SDK
cp -r openwrt/ <sdk>/package/net/udp2tcp-go

# 2. Install feed dependencies and select the package
cd <sdk>
./scripts/feeds update -a
./scripts/feeds install -a
make menuconfig   # → Network → VPN → udp2tcp-go

# 3. Compile
make package/udp2tcp-go/compile V=s

# Output: bin/packages/<arch>/base/udp2tcp-go_*.ipk
```

## OpenWrt install (pre-built binary)

```bash
# On the router (x86-64 example):
wget -O /usr/bin/udp2tcp \
  https://github.com/danny30au/udp2tcp-go/releases/latest/download/udp2tcp-linux-x86_64
chmod +x /usr/bin/udp2tcp

# Install init script and UCI config
wget -O /etc/init.d/udp2tcp \
  https://raw.githubusercontent.com/danny30au/udp2tcp-go/main/openwrt/files/udp2tcp.init
chmod +x /etc/init.d/udp2tcp

wget -O /etc/config/udp2tcp \
  https://raw.githubusercontent.com/danny30au/udp2tcp-go/main/openwrt/files/udp2tcp.config

# Edit UCI config
uci set udp2tcp.client.enabled=1
uci set udp2tcp.client.listen='127.0.0.1:51820'
uci set udp2tcp.client.remote='YOUR_SERVER_IP:51820'
uci set udp2tcp.client.threads=4
uci commit udp2tcp

/etc/init.d/udp2tcp enable
/etc/init.d/udp2tcp start
```

## Architecture

```
UDP→TCP mode (client side)
────────────────────────────────────────────────────────────────────────
 WireGuard (userspace) ──UDP──► [Worker goroutine 0 / OS thread 0]──┐
                        ◄──────                                      ├──► TCP (WG framing) ──► remote
                                [Worker goroutine 1 / OS thread 1]──┘
            ← each worker binds same UDP port via SO_REUSEPORT ─────►
            ← kernel hashes src IP+port → distributes across sockets ►

TCP→UDP mode (--reverse, server/endpoint side)
────────────────────────────────────────────────────────────────────────
 udp2tcp client ──TCP──► [TCP listener] ──UDP──► WireGuard server
                ◄──────                 ◄───────
```

### Platform socket code

The UDP socket setup is split into build-tag files:

| File | Build tag | Details |
|------|-----------|---------|
| `socket_linux.go` | `linux` | `golang.org/x/sys/unix` — correct `SO_REUSEPORT` constant for both glibc and musl (OpenWrt) |
| `socket_other.go` | `!linux` | Standard `syscall` pkg — `SO_REUSEPORT` not set (macOS/BSD/Windows fallback) |

### WireGuard-over-TCP framing

```
┌──────────────────────┬──────────────────────────────────┐
│  Length (2 bytes BE) │  WireGuard packet (1–65535 bytes)│
└──────────────────────┴──────────────────────────────────┘
```

Compatible with [wstunnel](https://github.com/erebe/wstunnel), wg-tcp-tunnel, and wireguard-tcp.

## Usage

### UDP → TCP (client side)

```bash
# WireGuard sends to 127.0.0.1:51820; udp2tcp forwards to server over TCP
udp2tcp -listen 127.0.0.1:51820 -remote YOUR_SERVER:51820 -threads 4
```

Set WireGuard `Endpoint = 127.0.0.1:51820`.

### TCP → UDP (server / endpoint side, `--reverse`)

```bash
udp2tcp -listen 0.0.0.0:51820 -remote 127.0.0.1:51820 -reverse -threads 4
```

### All flags

| Flag | Env var | Default | Description |
|------|---------|---------|-------------|
| `-listen` | `UDP2TCP_LISTEN` | `0.0.0.0:51820` | Local bind address |
| `-remote` | `UDP2TCP_REMOTE` | *(required)* | Remote forward address |
| `-reverse` | `UDP2TCP_REVERSE` | false | TCP→UDP mode |
| `-threads` | `UDP2TCP_THREADS` | # of CPUs | Worker goroutine count |
| `-udp-recv-buf` | `UDP2TCP_UDP_RECV_BUF` | 26214400 | UDP SO_RCVBUF (bytes) |
| `-udp-send-buf` | `UDP2TCP_UDP_SEND_BUF` | 26214400 | UDP SO_SNDBUF (bytes) |
| `-tcp-buf` | `UDP2TCP_TCP_BUF` | 4194304 | TCP SO_SNDBUF/SO_RCVBUF (bytes) |
| `-pkt-buf` | `UDP2TCP_PKT_BUF` | 65536 | Per-goroutine read buffer (bytes) |
| `-max-sessions` | `UDP2TCP_MAX_SESSIONS` | 65536 | Max concurrent UDP sessions |
| `-idle-timeout` | `UDP2TCP_IDLE_TIMEOUT` | 180 | Session idle timeout (seconds) |
| `-nodelay` | `UDP2TCP_NODELAY` | true | TCP_NODELAY |
| `-reuseport` | `UDP2TCP_REUSEPORT` | true | SO_REUSEPORT (Linux) |
| `-tcp-streams` | `UDP2TCP_TCP_STREAMS` | 1 | Parallel TCP connections per UDP session (>1 stripes packets across N streams for higher throughput) |
| `-log-level` | `UDP2TCP_LOG_LEVEL` | info | debug \| info \| warn \| error |

## Kernel tuning (OpenWrt / Linux)

```bash
# /etc/sysctl.d/99-udp2tcp.conf  (or uci-defaults on OpenWrt)
net.core.rmem_max = 134217728
net.core.wmem_max = 134217728
net.core.rmem_default = 16777216
net.core.wmem_default = 16777216
net.core.netdev_max_backlog = 5000
net.core.somaxconn = 65535
```

On OpenWrt via UCI:
```bash
uci set system.@system[0].conloglevel=8
echo "net.core.rmem_max=134217728" >> /etc/sysctl.d/99-udp2tcp.conf
sysctl --system
```

## systemd unit (non-OpenWrt Linux)

```ini
[Unit]
Description=udp2tcp WireGuard TCP proxy
After=network.target

[Service]
ExecStart=/usr/local/bin/udp2tcp -listen 127.0.0.1:51820 -remote 203.0.113.1:51820 -threads 4
Restart=always
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

## License

MIT
