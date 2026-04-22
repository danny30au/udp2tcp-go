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
			slog.Debug("new session", "worker", workerID, "client", srcAddr)
			go tcpForwardTask(cfg, udpConn, srcAddr, sess, table, workerID)
		}

		select {
		case sess.Ch <- session.Packet{Data: data}:
		default:
			metrics.IncErrors()
			slog.Debug("channel full, dropping packet", "client", srcAddr)
		}
	}
}

// tcpForwardTask is the per-session goroutine that owns one TCP connection.
//
// Two concurrent paths:
//  1. channel → TCP write (outbound WireGuard packets)
//  2. TCP read → UDP reply back to client (inbound WireGuard packets)
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

	tcpConn, err := net.Dial("tcp", cfg.Remote)
	if err != nil {
		metrics.IncErrors()
		slog.Error("TCP dial failed", "worker", workerID,
			"client", clientAddr, "remote", cfg.Remote, "err", err)
		return
	}
	defer tcpConn.Close()
	applyTCPOpts(tcpConn, cfg)

	slog.Info("TCP connected", "worker", workerID, "client", clientAddr, "remote", cfg.Remote)

	// Buffered writer — merges 2-byte header + payload into one write() for
	// frames ≤ ~64 KB; critical for keeping syscall count low.
	tcpWriter := bufio.NewWriterSize(tcpConn, 65536)

	done := make(chan struct{})
	closeDone := func() {
		select {
		case <-done:
		default:
			close(done)
		}
	}

	// Path A: TCP inbound → UDP reply.
	go func() {
		defer closeDone()
		readBuf := make([]byte, codec.MaxDatagramSize)
		for {
			frame, err := codec.ReadFrame(tcpConn, readBuf)
			if err != nil {
				if err != io.EOF {
					metrics.IncErrors()
					slog.Debug("TCP read error", "client", clientAddr, "err", err)
				}
				return
			}
			// Copy frame before sending — ReadFrame returns a sub-slice of readBuf.
			payload := make([]byte, len(frame))
			copy(payload, frame)
			if _, err := udpConn.WriteTo(payload, clientAddr); err != nil {
				metrics.IncErrors()
				slog.Debug("UDP WriteTo error", "client", clientAddr, "err", err)
				return
			}
			metrics.IncRx(uint64(len(payload)))
		}
	}()

	// Path B: channel → TCP outbound.
	for {
		select {
		case pkt, ok := <-sess.Ch:
			if !ok {
				return // channel closed by idle sweeper
			}
			if err := codec.WriteFrame(tcpWriter, pkt.Data); err != nil {
				metrics.IncErrors()
				slog.Debug("TCP write error", "client", clientAddr, "err", err)
				closeDone()
				return
			}
			if err := tcpWriter.Flush(); err != nil {
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
	ln, err := net.Listen("tcp", cfg.Listen)
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
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
