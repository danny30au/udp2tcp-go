// Package metrics provides lightweight atomic counters for operational visibility.
package metrics

import (
	"fmt"
	"sync/atomic"
)

var (
	PacketsRx    atomic.Uint64
	PacketsTx    atomic.Uint64
	BytesRx      atomic.Uint64
	BytesTx      atomic.Uint64
	SessionsActive atomic.Int64
	Errors       atomic.Uint64
)

func IncRx(bytes uint64) {
	PacketsRx.Add(1)
	BytesRx.Add(bytes)
}

func IncTx(bytes uint64) {
	PacketsTx.Add(1)
	BytesTx.Add(bytes)
}

func IncErrors() {
	Errors.Add(1)
}

// Summary returns a human-readable stats line.
func Summary() string {
	return fmt.Sprintf(
		"pkts_rx=%d pkts_tx=%d bytes_rx=%d bytes_tx=%d sessions=%d errors=%d",
		PacketsRx.Load(), PacketsTx.Load(),
		BytesRx.Load(), BytesTx.Load(),
		SessionsActive.Load(),
		Errors.Load(),
	)
}
