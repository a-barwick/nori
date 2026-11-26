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

type TraceEntry struct {
	Seq    int64
	Step   int
	At     time.Time
	UserID string
	Actor  string
	Action string
	Detail string
}

type Trace struct {
	mu       sync.Mutex
	capacity int
	entries  []TraceEntry
	head     int
	count    int
	total    int64
	digest   hash.Hash
}

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

func (t *Trace) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count
}

func (t *Trace) Total() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.total
}

func (t *Trace) Entries() []TraceEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastLocked(t.count)
}

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
