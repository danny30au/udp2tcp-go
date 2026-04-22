// Package config parses CLI flags and environment variables.
package config

import (
	"flag"
	"fmt"
	"net"
	"os"
	"runtime"
	"strconv"
)

// Config holds all runtime configuration.
type Config struct {
	Listen      string // local bind address
	Remote      string // remote forward address
	Reverse     bool   // TCP→UDP mode
	Threads     int    // OS worker threads
	UDPRecvBuf  int    // SO_RCVBUF for UDP sockets
	UDPSendBuf  int    // SO_SNDBUF for UDP sockets
	TCPBuf      int    // SO_RCVBUF/SO_SNDBUF for TCP sockets
	PktBuf      int    // per-goroutine packet read buffer size
	MaxSessions int    // max concurrent UDP sessions
	IdleTimeout int    // session idle timeout in seconds
	NoDelay     bool   // TCP_NODELAY
	ReusePort   bool   // SO_REUSEPORT on UDP sockets
	TCPStreams  int    // parallel TCP connections per UDP session (1 = single stream)
	CPUPin      bool   // pin goroutines to CPU (via GOMAXPROCS sharding)
	LogLevel    string // debug | info | warn | error
	Daemon      bool   // detach from terminal and run in background
	PIDFile     string // path to write PID file (empty disables)
	LogFile     string // path to log file (empty = stdout)
}

func Parse() (*Config, error) {
	cfg := &Config{}

	flag.StringVar(&cfg.Listen, "listen", env("UDP2TCP_LISTEN", "0.0.0.0:51820"), "Local bind address")
	flag.StringVar(&cfg.Remote, "remote", env("UDP2TCP_REMOTE", ""), "Remote forward address (required)")
	flag.BoolVar(&cfg.Reverse, "reverse", envBool("UDP2TCP_REVERSE", false), "TCP→UDP reverse mode")
	flag.IntVar(&cfg.Threads, "threads", envInt("UDP2TCP_THREADS", runtime.NumCPU()), "Worker thread count")
	flag.IntVar(&cfg.UDPRecvBuf, "udp-recv-buf", envInt("UDP2TCP_UDP_RECV_BUF", 26214400), "UDP SO_RCVBUF bytes")
	flag.IntVar(&cfg.UDPSendBuf, "udp-send-buf", envInt("UDP2TCP_UDP_SEND_BUF", 26214400), "UDP SO_SNDBUF bytes")
	flag.IntVar(&cfg.TCPBuf, "tcp-buf", envInt("UDP2TCP_TCP_BUF", 4194304), "TCP SO_SNDBUF/SO_RCVBUF bytes")
	flag.IntVar(&cfg.PktBuf, "pkt-buf", envInt("UDP2TCP_PKT_BUF", 65536), "Per-goroutine packet buffer bytes")
	flag.IntVar(&cfg.MaxSessions, "max-sessions", envInt("UDP2TCP_MAX_SESSIONS", 65536), "Max UDP client sessions")
	flag.IntVar(&cfg.IdleTimeout, "idle-timeout", envInt("UDP2TCP_IDLE_TIMEOUT", 180), "Session idle timeout (seconds)")
	flag.BoolVar(&cfg.NoDelay, "nodelay", envBool("UDP2TCP_NODELAY", true), "Enable TCP_NODELAY")
	flag.BoolVar(&cfg.ReusePort, "reuseport", envBool("UDP2TCP_REUSEPORT", true), "Enable SO_REUSEPORT (Linux)")
	flag.IntVar(&cfg.TCPStreams, "tcp-streams", envInt("UDP2TCP_TCP_STREAMS", 1), "Parallel TCP connections per UDP session (>=1)")
	flag.BoolVar(&cfg.CPUPin, "cpu-pin", envBool("UDP2TCP_CPU_PIN", false), "Shard workers across GOMAXPROCS")
	flag.StringVar(&cfg.LogLevel, "log-level", env("UDP2TCP_LOG_LEVEL", "info"), "Log level: debug|info|warn|error")
	flag.BoolVar(&cfg.Daemon, "daemon", envBool("UDP2TCP_DAEMON", false), "Detach from terminal and run as a background daemon")
	flag.StringVar(&cfg.PIDFile, "pidfile", env("UDP2TCP_PIDFILE", ""), "Write process PID to this file (removed on exit)")
	flag.StringVar(&cfg.LogFile, "log-file", env("UDP2TCP_LOG_FILE", ""), "Append logs to this file instead of stdout (required for diagnostics in -daemon mode)")

	flag.Parse()

	if cfg.Remote == "" {
		return nil, fmt.Errorf("--remote is required")
	}
	if _, _, err := net.SplitHostPort(cfg.Listen); err != nil {
		return nil, fmt.Errorf("invalid --listen %q: %w", cfg.Listen, err)
	}
	if _, _, err := net.SplitHostPort(cfg.Remote); err != nil {
		return nil, fmt.Errorf("invalid --remote %q: %w", cfg.Remote, err)
	}
	if cfg.Threads < 1 {
		cfg.Threads = 1
	}
	if cfg.TCPStreams < 1 {
		cfg.TCPStreams = 1
	}
	return cfg, nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		b, err := strconv.ParseBool(v)
		if err == nil {
			return b
		}
	}
	return def
}
