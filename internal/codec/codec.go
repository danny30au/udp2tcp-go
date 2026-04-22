// Package codec implements the WireGuard-over-TCP framing:
// a 2-byte big-endian length prefix followed by the raw WireGuard UDP payload.
//
//	┌──────────────────────┬──────────────────────────────────┐
//	│  Length (2 bytes BE) │  WireGuard packet (1–65535 bytes)│
//	└──────────────────────┴──────────────────────────────────┘
//
// This is the same wire format used by wstunnel, wg-tcp-tunnel, etc.
package codec

import (
	"encoding/binary"
	"fmt"
	"io"
)

const MaxDatagramSize = 65535

// ReadFrame reads exactly one length-prefixed frame from r.
// buf must be at least MaxDatagramSize bytes; the returned slice
// is a sub-slice of buf — no allocation on the hot path.
func ReadFrame(r io.Reader, buf []byte) ([]byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n == 0 || n > MaxDatagramSize {
		return nil, fmt.Errorf("invalid frame length: %d", n)
	}
	if len(buf) < n {
		return nil, fmt.Errorf("read buf too small: need %d have %d", n, len(buf))
	}
	if _, err := io.ReadFull(r, buf[:n]); err != nil {
		return nil, err
	}
	return buf[:n], nil
}

// WriteFrame writes a length-prefixed frame to w.
func WriteFrame(w io.Writer, data []byte) error {
	n := len(data)
	if n == 0 || n > MaxDatagramSize {
		return fmt.Errorf("invalid payload length: %d", n)
	}
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(n))
	// Use a single writev-style gather write where possible.
	// Fallback: two writes (header then body).
	if bw, ok := w.(interface {
		Write([]byte) (int, error)
	}); ok {
		// Combine into one syscall via a small stack buffer for the header.
		// For frames ≤ 1500 bytes (normal WG MTU) we merge header+body.
		if n <= 1498 {
			tmp := make([]byte, 2+n)
			copy(tmp[:2], hdr[:])
			copy(tmp[2:], data)
			_, err := bw.Write(tmp)
			return err
		}
	}
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}
