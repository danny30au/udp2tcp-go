package main

import (
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/danny30au/udp2tcp-go/internal/config"
	"github.com/danny30au/udp2tcp-go/internal/metrics"
	"github.com/danny30au/udp2tcp-go/internal/proxy"
)

var version = "0.1.0"

func main() {
	cfg, err := config.Parse()
	if err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}

	// If -daemon is set and we're still the foreground process, re-exec
	// ourselves detached and exit. The child sees UDP2TCP_DAEMONIZED=1
	// and falls through to normal startup below.
	if cfg.Daemon {
		isParent, derr := daemonize(cfg.PIDFile)
		if derr != nil {
			slog.Error("daemonize failed", "err", derr)
			os.Exit(1)
		}
		if isParent {
			return
		}
	} else if cfg.PIDFile != "" {
		// Non-daemon mode: still honour -pidfile so users can manage the
		// process with standard service tooling.
		if err := writePIDFile(cfg.PIDFile, os.Getpid()); err != nil {
			slog.Error("write pidfile", "path", cfg.PIDFile, "err", err)
			os.Exit(1)
		}
	}
	if cfg.PIDFile != "" {
		defer removePIDFile(cfg.PIDFile)
	}

	// Configure structured logging.
	lvl := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})))

	// Set GOMAXPROCS to the configured thread count.
	runtime.GOMAXPROCS(cfg.Threads)

	mode := "udp→tcp"
	if cfg.Reverse {
		mode = "tcp→udp (reverse)"
	}
	slog.Info("udp2tcp starting",
		"version", version,
		"mode", mode,
		"listen", cfg.Listen,
		"remote", cfg.Remote,
		"threads", cfg.Threads,
		"tcp_streams", cfg.TCPStreams,
		"reuseport", cfg.ReusePort,
	)

	if cfg.UDPRecvBuf > 4_194_304 {
		slog.Info("tip: raise kernel UDP buffers for full throughput — " +
			"net.core.rmem_max=134217728 net.core.wmem_max=134217728 net.core.netdev_max_backlog=5000")
	}

	// Periodic stats logger.
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			slog.Info("stats", "summary", metrics.Summary())
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		s := <-sigCh
		slog.Info("shutting down", "signal", s)
		slog.Info("final stats", "summary", metrics.Summary())
		removePIDFile(cfg.PIDFile)
		os.Exit(0)
	}()

	// Spawn cfg.Threads workers. Each worker gets its own OS goroutine
	// (runtime.LockOSThread) so the Go scheduler doesn't move it, and
	// each binds its own SO_REUSEPORT socket for kernel-level distribution.
	var wg sync.WaitGroup
	for i := 0; i < cfg.Threads; i++ {
		wg.Add(1)
		workerID := i
		go func() {
			defer wg.Done()
			runtime.LockOSThread()

			var runErr error
			if cfg.Reverse {
				runErr = proxy.RunTCPToUDP(cfg, workerID)
			} else {
				runErr = proxy.RunUDPToTCP(cfg, workerID)
			}
			if runErr != nil {
				slog.Error("worker exited", "worker", workerID, "err", runErr)
			}
		}()
	}
	wg.Wait()
}
