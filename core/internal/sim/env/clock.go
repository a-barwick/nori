package env

import (
	"encoding/binary"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/google/uuid"
)

// DefaultBaseTime is the fixed UTC base time used when NewClock is called with
// a zero time.
var DefaultBaseTime = time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)

// Clock is a mutex-safe deterministic virtual clock.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// NewClock returns a Clock initialized to start (normalized to UTC), or to
// DefaultBaseTime if start is zero.
func NewClock(start time.Time) *Clock {
	if start.IsZero() {
		start = DefaultBaseTime
	}
	return &Clock{now: start.UTC()}
}

// Now returns the current virtual time in UTC.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the virtual clock forward (or backward) by d and returns the
// updated UTC time.
func (c *Clock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}

// Set sets the virtual clock to t (normalized to UTC).
func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t.UTC()
}

// IDGen emits deterministic canonical RFC-4122 version-4 UUID strings from a
// seeded *math/rand/v2.Rand source.
type IDGen struct {
	mu  sync.Mutex
	rng *rand.Rand
}

// NewIDGen creates an IDGen backed by rng. If rng is nil, a fixed deterministic
// PCG source is used.
func NewIDGen(rng *rand.Rand) *IDGen {
	if rng == nil {
		rng = rand.New(rand.NewPCG(1, 1))
	}
	return &IDGen{rng: rng}
}

// NewIDGenFromSeed creates an IDGen seeded deterministically from seed.
func NewIDGenFromSeed(seed uint64) *IDGen {
	return NewIDGen(rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)))
}

// UUID returns the next deterministic RFC-4122 v4 uuid.UUID.
func (g *IDGen) UUID() uuid.UUID {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.rng == nil {
		g.rng = rand.New(rand.NewPCG(1, 1))
	}
	var raw [16]byte
	binary.LittleEndian.PutUint64(raw[0:8], g.rng.Uint64())
	binary.LittleEndian.PutUint64(raw[8:16], g.rng.Uint64())
	raw[6] = (raw[6] & 0x0f) | 0x40 // Version 4
	raw[8] = (raw[8] & 0x3f) | 0x80 // RFC-4122 variant (10xx)
	return uuid.UUID(raw)
}

// Next returns the next deterministic canonical RFC-4122 v4 UUID string.
func (g *IDGen) Next() string {
	return g.UUID().String()
}
