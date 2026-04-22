//go:build !linux

// Non-Linux fallback (macOS, BSD, Windows).
// SO_REUSEPORT is either unavailable or has different semantics.
// Buffer sizing is still applied where supported.
package proxy

import (
	"context"
	"log/slog"
	"net"
	"syscall"

	"github.com/danny30au/udp2tcp-go/internal/config"
)

func listenUDP(ctx context.Context, cfg *config.Config) (net.PacketConn, error) {
	lc := &net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			return c.Control(func(fd uintptr) {
				ifd := int(fd)
				_ = syscall.SetsockoptInt(ifd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
				_ = syscall.SetsockoptInt(ifd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, cfg.UDPRecvBuf)
				_ = syscall.SetsockoptInt(ifd, syscall.SOL_SOCKET, syscall.SO_SNDBUF, cfg.UDPSendBuf)
				if cfg.ReusePort {
					slog.Warn("SO_REUSEPORT not supported on this platform — using single socket")
				}
			})
		},
	}
	return lc.ListenPacket(ctx, "udp", cfg.Listen)
}
