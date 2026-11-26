package memstore

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
)

var (
	ErrInjectedFault = errors.New("memstore: injected fault")

	ErrCommitThenError = fmt.Errorf("%w: committed before error", ErrInjectedFault)

	ErrTxConflict = errors.New("memstore: serializable transaction conflict")

	ErrReadOnly = errors.New("memstore: cannot execute mutation in a read-only transaction")
)

type Float64Source interface {
	Float64() float64
}

type FaultConfig struct {
	GetErrProb float64

	QueryErrProb float64

	ApplyErrProb float64

	ArtifactErrProb float64

	TxConflictProb float64

	CommitThenErrProb float64

	LeaseErrProb float64
}

type FaultStats struct {
	GetErrors            int64
	QueryErrors          int64
	ApplyErrors          int64
	ArtifactErrors       int64
	TxConflicts          int64
	TxConflictsExhausted int64
	CommitThenErrors     int64
	LeaseErrors          int64
}

func (s FaultStats) Total() int64 {
	return s.GetErrors + s.QueryErrors + s.ApplyErrors + s.ArtifactErrors + s.TxConflicts + s.CommitThenErrors + s.LeaseErrors
}

type keyedFaultCounterKey struct {
	epoch uint64
	kind  string
	key   string
}

type FaultInjector struct {
	mu           sync.Mutex
	enabled      bool
	cfg          FaultConfig
	rng          Float64Source
	stats        FaultStats
	keyedEnabled bool
	keyedSeed    uint64
	keyedEpoch   uint64
	keyedCounts  map[keyedFaultCounterKey]uint64
}

func NewFaultInjector(cfg FaultConfig, rng Float64Source) *FaultInjector {
	if rng == nil {
		rng = rand.New(rand.NewPCG(1, 1))
	}
	return &FaultInjector{
		enabled: true,
		cfg:     cfg,
		rng:     rng,
	}
}

func (f *FaultInjector) SetEnabled(enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enabled = enabled
}

func (f *FaultInjector) Enabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabled
}

func (f *FaultInjector) SetConfig(cfg FaultConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg = cfg
}

func (f *FaultInjector) Configure(cfg FaultConfig, rng Float64Source) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg = cfg
	if rng != nil {
		f.rng = rng
	} else if f.rng == nil {
		f.rng = rand.New(rand.NewPCG(1, 1))
	}
	f.enabled = true
}

func (f *FaultInjector) Stats() FaultStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stats
}

func (f *FaultInjector) ResetStats() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stats = FaultStats{}
}

func (f *FaultInjector) SetKeyedMode(enabled bool, seed uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keyedEnabled = enabled
	f.keyedSeed = seed
	if enabled {
		if f.keyedCounts == nil {
			f.keyedCounts = make(map[keyedFaultCounterKey]uint64)
		}
	} else {
		f.keyedCounts = nil
	}
}

func (f *FaultInjector) KeyedMode() (bool, uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.keyedEnabled, f.keyedSeed
}

func (f *FaultInjector) SetKeyedEpoch(epoch uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keyedEpoch = epoch
}

func (f *FaultInjector) KeyedEpoch() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.keyedEpoch
}

func (f *FaultInjector) hitLocked(prob float64) bool {
	if !f.enabled || prob <= 0 {
		return false
	}
	if f.rng == nil {
		f.rng = rand.New(rand.NewPCG(1, 1))
	}
	return f.rng.Float64() < prob
}

func (f *FaultInjector) hitLockedFor(prob float64, kind, key string) bool {
	if !f.enabled || prob <= 0 {
		return false
	}
	if !f.keyedEnabled {
		return f.hitLocked(prob)
	}
	if f.keyedCounts == nil {
		f.keyedCounts = make(map[keyedFaultCounterKey]uint64)
	}
	ck := keyedFaultCounterKey{
		epoch: f.keyedEpoch,
		kind:  kind,
		key:   key,
	}
	seq := f.keyedCounts[ck]
	f.keyedCounts[ck] = seq + 1
	return keyedFloat64(f.keyedSeed, f.keyedEpoch, kind, key, seq) < prob
}

func keyedFloat64(seed, epoch uint64, kind, key string, seq uint64) float64 {
	h := sha256.New()
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], seed)
	h.Write(buf[:])
	binary.BigEndian.PutUint64(buf[:], epoch)
	h.Write(buf[:])
	binary.BigEndian.PutUint64(buf[:], uint64(len(kind)))
	h.Write(buf[:])
	h.Write([]byte(kind))
	binary.BigEndian.PutUint64(buf[:], uint64(len(key)))
	h.Write(buf[:])
	h.Write([]byte(key))
	binary.BigEndian.PutUint64(buf[:], seq)
	h.Write(buf[:])
	sum := h.Sum(nil)
	u := binary.BigEndian.Uint64(sum[:8]) >> 11
	return float64(u) / (1 << 53)
}

func (f *FaultInjector) maybeGetErrFor(key string) error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hitLockedFor(f.cfg.GetErrProb, "get", key) {
		f.stats.GetErrors++
		return fmt.Errorf("%w: get", ErrInjectedFault)
	}
	return nil
}

func (f *FaultInjector) maybeQueryErrFor(key string) error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hitLockedFor(f.cfg.QueryErrProb, "query", key) {
		f.stats.QueryErrors++
		return fmt.Errorf("%w: query", ErrInjectedFault)
	}
	return nil
}

func (f *FaultInjector) maybeApplyErrFor(key string) error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hitLockedFor(f.cfg.ApplyErrProb, "apply", key) {
		f.stats.ApplyErrors++
		return fmt.Errorf("%w: apply", ErrInjectedFault)
	}
	return nil
}

func (f *FaultInjector) maybeArtifactErr() error {
	return f.maybeArtifactErrFor("")
}

func (f *FaultInjector) maybeArtifactErrFor(key string) error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hitLockedFor(f.cfg.ArtifactErrProb, "artifact", key) {
		f.stats.ArtifactErrors++
		return fmt.Errorf("%w: artifact", ErrInjectedFault)
	}
	return nil
}

func (f *FaultInjector) maybeTxConflictFor(key string) error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hitLockedFor(f.cfg.TxConflictProb, "tx_conflict", key) {
		f.stats.TxConflicts++
		return ErrTxConflict
	}
	return nil
}

func (f *FaultInjector) recordTxConflictExhausted() {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stats.TxConflictsExhausted++
}

func (f *FaultInjector) maybeCommitThenErr() error {
	return f.maybeCommitThenErrFor("")
}

func (f *FaultInjector) maybeCommitThenErrFor(key string) error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hitLockedFor(f.cfg.CommitThenErrProb, "commit_then_err", key) {
		f.stats.CommitThenErrors++
		return ErrCommitThenError
	}
	return nil
}

func (f *FaultInjector) maybeLeaseErrFor(key string) error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hitLockedFor(f.cfg.LeaseErrProb, "lease", key) {
		f.stats.LeaseErrors++
		return fmt.Errorf("%w: lease", ErrInjectedFault)
	}
	return nil
}
