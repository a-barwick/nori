package env

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
	"sync"
	"time"
)

// TraceEntry is a single recorded simulation event.
type TraceEntry struct {
	Seq    int64
	Step   int
	At     time.Time
	UserID string
	Actor  string
	Action string
	Detail string
}

// Trace is a lightweight deterministic event logger for simulation steps.
// When created with a positive capacity it retains the most recent capacity
// entries in a ring buffer while still hashing every recorded entry into
// Digest(). When capacity <= 0 it retains all entries.
type Trace struct {
	mu       sync.Mutex
	capacity int
	entries  []TraceEntry
	head     int
	count    int
	total    int64
	digest   hash.Hash
}

// NewTrace creates a Trace. A positive capacity bounds retained entries to a
// ring buffer of that size; capacity <= 0 retains all entries.
func NewTrace(capacity int) *Trace {
	h := sha256.New()
	writeTag(h, "sim.env.trace.v1")
	t := &Trace{
		capacity: capacity,
		digest:   h,
	}
	if capacity > 0 {
		t.entries = make([]TraceEntry, capacity)
	}
	return t
}

// Record appends one deterministic event to the trace.
func (t *Trace) Record(step int, at time.Time, userID, actor, action, detail string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.total++
	entry := TraceEntry{
		Seq:    t.total,
		Step:   step,
		At:     at.UTC(),
		UserID: userID,
		Actor:  actor,
		Action: action,
		Detail: detail,
	}

	writeInt64(t.digest, entry.Seq)
	writeInt64(t.digest, int64(entry.Step))
	writeTime(t.digest, entry.At)
	writeString(t.digest, entry.UserID)
	writeString(t.digest, entry.Actor)
	writeString(t.digest, entry.Action)
	writeString(t.digest, entry.Detail)

	if t.capacity > 0 {
		idx := (t.head + t.count) % t.capacity
		t.entries[idx] = entry
		if t.count < t.capacity {
			t.count++
		} else {
			t.head = (t.head + 1) % t.capacity
		}
		return
	}
	t.entries = append(t.entries, entry)
	t.count = len(t.entries)
}

// Len returns the number of currently retained trace entries.
func (t *Trace) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count
}

// Total returns the total number of entries recorded over the trace's lifetime,
// including any evicted from a bounded ring buffer.
func (t *Trace) Total() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.total
}

// Entries returns a chronological copy of all retained entries.
func (t *Trace) Entries() []TraceEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastLocked(t.count)
}

// FormatLast formats the last n retained entries as newline-separated diagnostic
// lines. If n <= 0 or n > retained count, all retained entries are formatted.
func (t *Trace) FormatLast(n int) string {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.count == 0 {
		return ""
	}
	if n <= 0 || n > t.count {
		n = t.count
	}
	items := t.lastLocked(n)
	var b strings.Builder
	for i, e := range items {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "#%d step=%d t=%s user=%s actor=%s action=%s",
			e.Seq, e.Step, e.At.UTC().Format(time.RFC3339Nano), e.UserID, e.Actor, e.Action)
		if e.Detail != "" {
			b.WriteString(" detail=")
			b.WriteString(e.Detail)
		}
	}
	return b.String()
}

// Digest returns a canonical hex-encoded SHA-256 hash over every trace entry
// recorded since the Trace was created.
func (t *Trace) Digest() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return hex.EncodeToString(t.digest.Sum(nil))
}

func (t *Trace) lastLocked(n int) []TraceEntry {
	if n <= 0 || t.count == 0 {
		return nil
	}
	if n > t.count {
		n = t.count
	}
	out := make([]TraceEntry, n)
	if t.capacity > 0 {
		start := (t.head + (t.count - n)) % t.capacity
		for i := range n {
			out[i] = t.entries[(start+i)%t.capacity]
		}
		return out
	}
	copy(out, t.entries[t.count-n:])
	return out
}
