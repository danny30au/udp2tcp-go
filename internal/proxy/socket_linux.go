//go:build linux

package proxy

import (
	"context"
	"log/slog"
	"net"
	"syscall"

	"github.com/danny30au/udp2tcp-go/internal/config"
	"golang.org/x/sys/unix"
)

// listenUDP binds a UDP socket on Linux with:
//   - SO_REUSEADDR  — allows rapid rebind after restart
//   - SO_REUSEPORT  — allows N worker goroutines to share the same port;
//     the kernel distributes datagrams across all sockets via its internal
//     hash (equivalent to RSS in software). This is the core of multi-core
//     scaling: no userspace lock on the receive path.
//   - SO_RCVBUF / SO_SNDBUF  — raised to cfg values (default 25 MB each)
//
// Uses golang.org/x/sys/unix so the correct constant values are used for
// both glibc and musl (OpenWrt), avoiding the hard-coded 0xf hack.
func listenUDP(ctx context.Context, cfg *config.Config) (net.PacketConn, error) {
	lc := &net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			return c.Control(func(fd uintptr) {
				ifd := int(fd)

				if err := unix.SetsockoptInt(ifd, unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); err != nil {
					slog.Warn("SO_REUSEADDR failed", "err", err)
				}

				if cfg.ReusePort {
					if err := unix.SetsockoptInt(ifd, unix.SOL_SOCKET, unix.SO_REUSEPORT, 1); err != nil {
						slog.Warn("SO_REUSEPORT not available", "err", err)
					}
				}

				// Kernel buffer sizes — best effort; silently ignored if
				// the kernel caps them below the requested value.
				// sysctl net.core.rmem_max must be >= cfg.UDPRecvBuf for
				// the full value to take effect.
				_ = unix.SetsockoptInt(ifd, unix.SOL_SOCKET, unix.SO_RCVBUF, cfg.UDPRecvBuf)
				_ = unix.SetsockoptInt(ifd, unix.SOL_SOCKET, unix.SO_SNDBUF, cfg.UDPSendBuf)
			})
		},
	}
	return lc.ListenPacket(ctx, "udp", cfg.Listen)
}
