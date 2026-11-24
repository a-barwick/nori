// Package memstore provides a deterministic in-memory docstore.Backend and
// state.ArtifactStore with fault injection and canonical snapshot hashing for
// Nori's deterministic simulation tests.
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
	// ErrInjectedFault is returned when a configured fault triggers.
	ErrInjectedFault = errors.New("memstore: injected fault")

	// ErrCommitThenError is returned when a commit-then-error fault triggers
	// after a mutation has been durably applied to in-memory state. It wraps
	// ErrInjectedFault.
	ErrCommitThenError = fmt.Errorf("%w: committed before error", ErrInjectedFault)

	// ErrTxConflict indicates a serializable transaction conflict.
	ErrTxConflict = errors.New("memstore: serializable transaction conflict")

	// ErrReadOnly indicates an attempted write inside a read-only transaction.
	ErrReadOnly = errors.New("memstore: cannot execute mutation in a read-only transaction")
)

// Float64Source supplies deterministic pseudo-random numbers in [0.0, 1.0).
// *math/rand/v2.Rand implements this interface directly.
type Float64Source interface {
	Float64() float64
}

// FaultConfig configures deterministic fault injection probabilities in [0.0, 1.0].
type FaultConfig struct {
	// GetErrProb is the probability of a transient error on Get or CurrentSequence.
	GetErrProb float64

	// QueryErrProb is the probability of a transient error on Query.
	QueryErrProb float64

	// ApplyErrProb is the probability of a transient error on Apply or DeleteBatch
	// before state is mutated.
	ApplyErrProb float64

	// ArtifactErrProb is the probability of a transient error on Artifacts
	// operations before state is mutated.
	ArtifactErrProb float64

	// TxConflictProb is the probability of a retryable serializable conflict
	// during RunTransaction commit. RunTransaction retries up to its configured
	// attempt limit before surfacing ErrTxConflict.
	TxConflictProb float64

	// CommitThenErrProb is the probability that a durable mutation
	// (RunTransaction commit with writes, Apply, DeleteBatch, PutPNG, PutFile,
	// or DeleteFile when a key existed) commits durably to state and then
	// returns ErrCommitThenError to the caller.
	CommitThenErrProb float64

	// LeaseErrProb is the probability of a transient error on standalone
	// docstore.LeaseStore operations (claims, renewals, parks, releases,
	// deletes, lists, and non-transactional wakes).
	LeaseErrProb float64
}

// FaultStats records counts of injected faults for test assertions and telemetry.
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

// Total returns the sum of all injected fault events (excluding TxConflictsExhausted,
// which is a subset outcome of TxConflicts).
func (s FaultStats) Total() int64 {
	return s.GetErrors + s.QueryErrors + s.ApplyErrors + s.ArtifactErrors + s.TxConflicts + s.CommitThenErrors + s.LeaseErrors
}

// FaultInjector drives deterministic fault injection for one or more stores.
// A single FaultInjector may be shared across a Backend and Artifacts instance
// so that SetEnabled(false) disables faults globally during quiescence drain.
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

// NewFaultInjector creates an enabled FaultInjector with the given config and RNG.
// If rng is nil, a fixed deterministic PCG source is used.
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

// SetEnabled enables or disables all fault injection without resetting config or stats.
func (f *FaultInjector) SetEnabled(enabled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enabled = enabled
}

// Enabled reports whether fault injection is currently active.
func (f *FaultInjector) Enabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabled
}

// SetConfig updates the fault probabilities while keeping the existing RNG and stats.
func (f *FaultInjector) SetConfig(cfg FaultConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cfg = cfg
}

// Configure updates both the fault probabilities and the RNG source (if non-nil).
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

// Stats returns a snapshot of injected fault counters.
func (f *FaultInjector) Stats() FaultStats {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stats
}

// ResetStats zeroes all injected fault counters.
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

func (f *FaultInjector) maybeGetErr() error {
	return f.maybeGetErrFor("")
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

func (f *FaultInjector) maybeQueryErr() error {
	return f.maybeQueryErrFor("")
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

func (f *FaultInjector) maybeApplyErr() error {
	return f.maybeApplyErrFor("")
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

func (f *FaultInjector) maybeTxConflict() error {
	return f.maybeTxConflictFor("")
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
