// Package session manages per-UDP-client session state.
//
// Each unique (src_ip, src_port) in UDP→TCP mode gets a Session entry.
// Sessions are stored in a sharded map to reduce lock contention across
// multiple worker goroutines.
package session

import (
	"net"
	"sync"
	"time"
)

const shards = 256 // must be power of two

// Packet is the unit passed through the per-session channel.
type Packet struct {
	Data []byte
}

// Session holds state for a single UDP client.
type Session struct {
	Ch       chan Packet
	LastSeen time.Time
	mu       sync.Mutex
}

func (s *Session) Touch() {
	s.mu.Lock()
	s.LastSeen = time.Now()
	s.mu.Unlock()
}

func (s *Session) IsIdle(timeout time.Duration) bool {
	s.mu.Lock()
	idle := time.Since(s.LastSeen) > timeout
	s.mu.Unlock()
	return idle
}

// Table is a sharded concurrent map of SocketAddr → *Session.
type Table struct {
	shards  [shards]shard
	max     int
	timeout time.Duration
}

type shard struct {
	mu      sync.RWMutex
	entries map[string]*Session
}

func NewTable(max int, timeout time.Duration) *Table {
	t := &Table{max: max, timeout: timeout}
	for i := range t.shards {
		t.shards[i].entries = make(map[string]*Session, max/shards+1)
	}
	return t
}

func (t *Table) shardFor(addr net.Addr) *shard {
	// Simple hash: sum the bytes of the string key.
	key := addr.String()
	h := uint32(0)
	for i := 0; i < len(key); i++ {
		h = h*31 + uint32(key[i])
	}
	return &t.shards[h&(shards-1)]
}

// Get returns the session for addr, touching its timestamp.
func (t *Table) Get(addr net.Addr) (*Session, bool) {
	s := t.shardFor(addr)
	s.mu.RLock()
	sess, ok := s.entries[addr.String()]
	s.mu.RUnlock()
	if ok {
		sess.Touch()
	}
	return sess, ok
}

// GetOrCreate returns an existing session or creates a new one.
// created=true means a new session was inserted; the caller must start
// the associated goroutine.
// Returns nil, false, false if the table is full.
func (t *Table) GetOrCreate(addr net.Addr, chanBuf int) (*Session, bool, bool) {
	s := t.shardFor(addr)
	key := addr.String()

	s.mu.RLock()
	sess, ok := s.entries[key]
	s.mu.RUnlock()
	if ok {
		sess.Touch()
		return sess, true, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Double-check after acquiring write lock.
	if sess, ok = s.entries[key]; ok {
		sess.Touch()
		return sess, true, false
	}
	// Check capacity.
	total := t.totalLen()
	if total >= t.max {
		return nil, false, false
	}
	sess = &Session{
		Ch:       make(chan Packet, chanBuf),
		LastSeen: time.Now(),
	}
	s.entries[key] = sess
	return sess, true, true
}

func (t *Table) Remove(addr net.Addr) {
	s := t.shardFor(addr)
	s.mu.Lock()
	delete(s.entries, addr.String())
	s.mu.Unlock()
}

// SweepIdle removes sessions idle longer than the configured timeout.
// Returns the number of sessions removed.
func (t *Table) SweepIdle() int {
	removed := 0
	for i := range t.shards {
		s := &t.shards[i]
		s.mu.Lock()
		for k, sess := range s.entries {
			if sess.IsIdle(t.timeout) {
				close(sess.Ch)
				delete(s.entries, k)
				removed++
			}
		}
		s.mu.Unlock()
	}
	return removed
}

func (t *Table) Len() int {
	return t.totalLen()
}

func (t *Table) totalLen() int {
	n := 0
	for i := range t.shards {
		t.shards[i].mu.RLock()
		n += len(t.shards[i].entries)
		t.shards[i].mu.RUnlock()
	}
	return n
}
