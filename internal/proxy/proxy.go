// Package proxy implements two proxy modes:
//
//   - UDP→TCP (default, client side): listens for UDP datagrams, wraps them
//     with a 2-byte big-endian length prefix and streams over TCP.
//
//   - TCP→UDP (--reverse, server side): accepts TCP connections, decodes
//     length-prefixed frames and emits raw UDP datagrams.
//
// Both modes use SO_REUSEPORT (Linux) to distribute traffic across multiple
// worker goroutines at the kernel level, giving near-linear multi-core scaling.
package proxy

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/danny30au/udp2tcp-go/internal/codec"
	"github.com/danny30au/udp2tcp-go/internal/config"
	"github.com/danny30au/udp2tcp-go/internal/metrics"
	"github.com/danny30au/udp2tcp-go/internal/session"
)

// RunUDPToTCP runs one worker in UDP→TCP proxy mode.
func RunUDPToTCP(cfg *config.Config, workerID int) error {
	udpConn, err := listenUDP(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer udpConn.Close()

	slog.Info("UDP listener ready", "worker", workerID, "listen", cfg.Listen)

	table := session.NewTable(
		maxInt(cfg.MaxSessions/maxInt(cfg.Threads, 1), 64),
		time.Duration(cfg.IdleTimeout)*time.Second,
	)

	// Idle sweeper.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			n := table.SweepIdle()
			if n > 0 {
				metrics.SessionsActive.Add(-int64(n))
				slog.Debug("swept idle sessions", "worker", workerID, "count", n)
			}
		}
	}()

	pktBuf := make([]byte, cfg.PktBuf)

	for {
		n, srcAddr, err := udpConn.ReadFrom(pktBuf)
		if err != nil {
			metrics.IncErrors()
			slog.Error("UDP ReadFrom", "worker", workerID, "err", err)
			continue
		}

		// Copy datagram — pktBuf is reused on the next iteration.
		data := make([]byte, n)
		copy(data, pktBuf[:n])
		metrics.IncRx(uint64(n))

		sess, ok, created := table.GetOrCreate(srcAddr, 4096)
		if !ok {
			metrics.IncErrors()
			slog.Warn("session table full, dropping packet", "worker", workerID, "src", srcAddr)
			continue
		}

		if created {
			metrics.SessionsActive.Add(1)
			slog.Info("new session", "worker", workerID, "client", srcAddr)
			go tcpForwardTask(cfg, udpConn, srcAddr, sess, table, workerID)
		}

		sent, closed := sess.TrySend(session.Packet{Data: data})
		if closed {
			// Sweeper closed the session between our GetOrCreate and now.
			// Drop the packet; a subsequent packet from the same client will
			// create a fresh session.
			metrics.IncErrors()
			slog.Debug("session closed during send, dropping packet", "client", srcAddr)
		} else if !sent {
			metrics.IncErrors()
			slog.Debug("channel full, dropping packet", "client", srcAddr)
		}
	}
}

// tcpForwardTask is the per-session goroutine that owns one or more TCP
// connections to the remote.
//
// When cfg.TCPStreams == 1 (default), a single TCP connection is used and the
// behaviour is identical to the original single-stream implementation.
//
// When cfg.TCPStreams > 1, N parallel TCP connections are dialed and outbound
// UDP→TCP packets are striped round-robin across them. This increases
// throughput when a single TCP flow becomes the bottleneck (per-flow
// congestion control, single-CPU softirq queue, middlebox per-flow shaping).
// One reader goroutine per stream forwards inbound TCP frames back as UDP.
//
// The wire format is unchanged — each stream independently carries
// length-prefixed wstunnel-compatible frames.
func tcpForwardTask(
	cfg *config.Config,
	udpConn net.PacketConn,
	clientAddr net.Addr,
	sess *session.Session,
	table *session.Table,
	workerID int,
) {
	defer func() {
		table.Remove(clientAddr)
		metrics.SessionsActive.Add(-1)
		slog.Debug("session torn down", "client", clientAddr)
	}()

	streams := cfg.TCPStreams
	if streams < 1 {
		streams = 1
	}

	// Dial all TCP streams up-front. If any dial fails, abort the session
	// (closing any successfully-dialed conns) — partial sessions would mask
	// failures from the operator and skew throughput.
	conns := make([]*net.TCPConn, 0, streams)
	writers := make([]*bufio.Writer, 0, streams)
	closeAll := func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}
	// dialTimeout bounds how long a single TCP connect may block. Without
	// this, net.Dial inherits the OS TCP connect timeout (~75-130s on Linux
	// when SYNs are silently dropped), which delays error visibility long
	// past the point an operator gives up debugging.
	dialer := &net.Dialer{Timeout: 10 * time.Second}

	for i := 0; i < streams; i++ {
		c, err := dialer.Dial("tcp", cfg.Remote)
		if err != nil {
			metrics.IncErrors()
			slog.Error("TCP dial failed", "worker", workerID,
				"client", clientAddr, "remote", cfg.Remote,
				"stream", i, "err", err)
			closeAll()
			return
		}
		applyTCPOpts(c, cfg)
		tc, ok := c.(*net.TCPConn)
		if !ok {
			// net.Dial("tcp", ...) always returns *net.TCPConn in the standard
			// library; this guard is defensive against future changes.
			metrics.IncErrors()
			slog.Error("unexpected non-TCP connection",
				"worker", workerID, "client", clientAddr, "stream", i)
			_ = c.Close()
			closeAll()
			return
		}
		conns = append(conns, tc)
		// Buffered writer — merges 2-byte header + payload into one write()
		// for frames ≤ ~64 KB; critical for keeping syscall count low.
		writers = append(writers, bufio.NewWriterSize(c, 65536))
	}
	defer closeAll()

	slog.Info("TCP connected",
		"worker", workerID, "client", clientAddr,
		"remote", cfg.Remote, "streams", streams)

	done := make(chan struct{})
	var doneOnce sync.Once
	closeDone := func() {
		doneOnce.Do(func() { close(done) })
	}

	// Path A: TCP inbound → UDP reply, one reader per stream.
	// net.PacketConn.WriteTo is safe for concurrent use.
	for i := range conns {
		c := conns[i]
		go func(streamID int) {
			defer closeDone()
			readBuf := make([]byte, codec.MaxDatagramSize)
			for {
				frame, err := codec.ReadFrame(c, readBuf)
				if err != nil {
					if err != io.EOF {
						metrics.IncErrors()
						slog.Debug("TCP read error",
							"client", clientAddr, "stream", streamID, "err", err)
					}
					return
				}
				// Copy frame before sending — ReadFrame returns a sub-slice of readBuf.
				payload := make([]byte, len(frame))
				copy(payload, frame)
				if _, err := udpConn.WriteTo(payload, clientAddr); err != nil {
					metrics.IncErrors()
					slog.Debug("UDP WriteTo error",
						"client", clientAddr, "stream", streamID, "err", err)
					return
				}
				metrics.IncRx(uint64(len(payload)))
			}
		}(i)
	}

	// Path B: channel → TCP outbound, round-robin across streams.
	next := 0
	for {
		select {
		case pkt, ok := <-sess.Ch:
			if !ok {
				return // channel closed by idle sweeper
			}
			w := writers[next]
			next = (next + 1) % streams
			if err := codec.WriteFrame(w, pkt.Data); err != nil {
				metrics.IncErrors()
				slog.Debug("TCP write error", "client", clientAddr, "err", err)
				closeDone()
				return
			}
			if err := w.Flush(); err != nil {
				metrics.IncErrors()
				closeDone()
				return
			}
			metrics.IncTx(uint64(len(pkt.Data)))
		case <-done:
			return
		}
	}
}

// ─── Mode B: TCP → UDP (--reverse) ──────────────────────────────────────────

// RunTCPToUDP accepts TCP connections and forwards each WireGuard frame as
// a raw UDP datagram to cfg.Remote, sending replies back as TCP frames.
func RunTCPToUDP(cfg *config.Config, workerID int) error {
	ln, err := listenTCP(context.Background(), cfg)
	if err != nil {
		return err
	}
	defer ln.Close()

	slog.Info("TCP listener ready (reverse mode)", "worker", workerID, "listen", cfg.Listen)

	for {
		conn, err := ln.Accept()
		if err != nil {
			metrics.IncErrors()
			slog.Error("TCP accept", "worker", workerID, "err", err)
			continue
		}
		applyTCPOpts(conn, cfg)
		go handleReverseClient(cfg, conn, workerID)
	}
}

func handleReverseClient(cfg *config.Config, tcpConn net.Conn, workerID int) {
	defer tcpConn.Close()
	peer := tcpConn.RemoteAddr()

	udpConn, err := net.Dial("udp", cfg.Remote)
	if err != nil {
		metrics.IncErrors()
		slog.Error("UDP dial failed", "peer", peer, "err", err)
		return
	}
	defer udpConn.Close()

	slog.Info("reverse session started", "worker", workerID, "peer", peer, "remote", cfg.Remote)

	done := make(chan struct{})
	closeDone := func() {
		select {
		case <-done:
		default:
			close(done)
		}
	}

	tcpWriter := bufio.NewWriterSize(tcpConn, 65536)
	readBuf := make([]byte, cfg.PktBuf)

	// UDP replies → TCP frames.
	go func() {
		defer closeDone()
		udpBuf := make([]byte, cfg.PktBuf)
		for {
			n, err := udpConn.Read(udpBuf)
			if err != nil {
				if err != io.EOF {
					metrics.IncErrors()
				}
				return
			}
			if err := codec.WriteFrame(tcpWriter, udpBuf[:n]); err != nil {
				metrics.IncErrors()
				return
			}
			if err := tcpWriter.Flush(); err != nil {
				metrics.IncErrors()
				return
			}
			metrics.IncTx(uint64(n))
		}
	}()

	// TCP frames → UDP datagrams.
	for {
		select {
		case <-done:
			return
		default:
		}
		frame, err := codec.ReadFrame(tcpConn, readBuf)
		if err != nil {
			if err != io.EOF {
				metrics.IncErrors()
				slog.Debug("TCP read error", "peer", peer, "err", err)
			}
			closeDone()
			return
		}
		payload := make([]byte, len(frame))
		copy(payload, frame)
		if _, err := udpConn.Write(payload); err != nil {
			metrics.IncErrors()
			closeDone()
			return
		}
		metrics.IncRx(uint64(len(payload)))
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func applyTCPOpts(conn net.Conn, cfg *config.Config) {
	if tc, ok := conn.(*net.TCPConn); ok {
		if cfg.NoDelay {
			_ = tc.SetNoDelay(true)
		}
		_ = tc.SetReadBuffer(cfg.TCPBuf)
		_ = tc.SetWriteBuffer(cfg.TCPBuf)
		// Detect silently-dropped TCP sessions (stateful NAT/firewall idle
		// expiry is the most common cause). Without keep-alives, both ends
		// can sit on a half-open connection indefinitely with no traffic
		// flowing in either direction.
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(30 * time.Second)
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
