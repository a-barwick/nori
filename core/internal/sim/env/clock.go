package env

import (
	"encoding/binary"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/google/uuid"
)

var DefaultBaseTime = time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)

type Clock struct {
	mu  sync.Mutex
	now time.Time
}

func NewClock(start time.Time) *Clock {
	if start.IsZero() {
		start = DefaultBaseTime
	}
	return &Clock{now: start.UTC()}
}

func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *Clock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	return c.now
}

func (c *Clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t.UTC()
}

type IDGen struct {
	mu  sync.Mutex
	rng *rand.Rand
}

func NewIDGen(rng *rand.Rand) *IDGen {
	if rng == nil {
		rng = rand.New(rand.NewPCG(1, 1))
	}
	return &IDGen{rng: rng}
}

func NewIDGenFromSeed(seed uint64) *IDGen {
	return NewIDGen(rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)))
}

func (g *IDGen) UUID() uuid.UUID {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.rng == nil {
		g.rng = rand.New(rand.NewPCG(1, 1))
	}
	var raw [16]byte
	binary.LittleEndian.PutUint64(raw[0:8], g.rng.Uint64())
	binary.LittleEndian.PutUint64(raw[8:16], g.rng.Uint64())
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return uuid.UUID(raw)
}

func (g *IDGen) Next() string {
	return g.UUID().String()
}
