package memstore

import (
	"bytes"
	"context"
	"sync"

	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

// StoredFile represents a stored uploaded file and its media type.
type StoredFile struct {
	MediaType string
	Data      []byte
}

// ArtifactOption configures a new Artifacts store.
type ArtifactOption func(*Artifacts)

// WithArtifactsFaultInjector attaches an existing FaultInjector to Artifacts.
func WithArtifactsFaultInjector(inj *FaultInjector) ArtifactOption {
	return func(a *Artifacts) {
		a.faults = inj
	}
}

// WithArtifactsFaults configures fault injection on Artifacts using cfg and rng.
func WithArtifactsFaults(cfg FaultConfig, rng Float64Source) ArtifactOption {
	return func(a *Artifacts) {
		a.faults = NewFaultInjector(cfg, rng)
	}
}

// Artifacts is a deterministic, in-memory implementation of state.ArtifactStore
// (embedding state.Artifacts) using deep copies of all byte slices.
type Artifacts struct {
	mu     sync.Mutex
	pngs   map[string][]byte     // key: "<run>/<id>.png"
	files  map[string]StoredFile // key: file key
	faults *FaultInjector
}

var (
	_ state.Artifacts     = (*Artifacts)(nil)
	_ state.ArtifactStore = (*Artifacts)(nil)
)

// NewArtifacts creates an empty in-memory artifact store.
func NewArtifacts(opts ...ArtifactOption) *Artifacts {
	a := &Artifacts{
		pngs:  map[string][]byte{},
		files: map[string]StoredFile{},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(a)
		}
	}
	return a
}

// SetFaults configures or replaces fault injection on this Artifacts store.
func (a *Artifacts) SetFaults(cfg FaultConfig, rng Float64Source) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.faults == nil {
		a.faults = NewFaultInjector(cfg, rng)
		return
	}
	a.faults.Configure(cfg, rng)
}

// SetFaultsEnabled toggles fault injection on or off without losing config or stats.
func (a *Artifacts) SetFaultsEnabled(enabled bool) {
	a.mu.Lock()
	inj := a.faults
	a.mu.Unlock()
	if inj != nil {
		inj.SetEnabled(enabled)
	}
}

// FaultStats returns injected fault counters for this Artifacts store.
func (a *Artifacts) FaultStats() FaultStats {
	a.mu.Lock()
	inj := a.faults
	a.mu.Unlock()
	if inj == nil {
		return FaultStats{}
	}
	return inj.Stats()
}

func (a *Artifacts) faultInjector() *FaultInjector {
	a.mu.Lock()
	inj := a.faults
	a.mu.Unlock()
	return inj
}

func pngKey(run, id string) string {
	return run + "/" + id + ".png"
}

func cloneBytes(b []byte) []byte {
	if len(b) == 0 {
		return []byte{}
	}
	return bytes.Clone(b)
}

// PutPNG stores a PNG artifact once under run/id and refuses to replace an existing one.
func (a *Artifacts) PutPNG(ctx context.Context, run, id string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	inj := a.faultInjector()
	if err := inj.maybeArtifactErr(); err != nil {
		return err
	}
	key := pngKey(run, id)
	a.mu.Lock()
	if _, exists := a.pngs[key]; exists {
		a.mu.Unlock()
		return state.ErrFileExists
	}
	a.pngs[key] = cloneBytes(data)
	a.mu.Unlock()
	if err := inj.maybeCommitThenErr(); err != nil {
		return err
	}
	return nil
}

// GetPNG retrieves a copy of a stored PNG artifact, returning state.ErrNotFound if absent.
func (a *Artifacts) GetPNG(ctx context.Context, run, id string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := a.faultInjector().maybeArtifactErr(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	data, ok := a.pngs[pngKey(run, id)]
	a.mu.Unlock()
	if !ok {
		return nil, state.ErrNotFound
	}
	return state.ReadArtifact(bytes.NewReader(cloneBytes(data)))
}

// PutFile stores a file once under key and returns state.ErrFileExists when the key is taken.
func (a *Artifacts) PutFile(ctx context.Context, key, mediaType string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	inj := a.faultInjector()
	if err := inj.maybeArtifactErr(); err != nil {
		return err
	}
	a.mu.Lock()
	if _, exists := a.files[key]; exists {
		a.mu.Unlock()
		return state.ErrFileExists
	}
	a.files[key] = StoredFile{
		MediaType: mediaType,
		Data:      cloneBytes(data),
	}
	a.mu.Unlock()
	if err := inj.maybeCommitThenErr(); err != nil {
		return err
	}
	return nil
}

// GetFile retrieves a copy of a stored file, returning state.ErrNotFound if absent.
func (a *Artifacts) GetFile(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := a.faultInjector().maybeArtifactErr(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	file, ok := a.files[key]
	a.mu.Unlock()
	if !ok {
		return nil, state.ErrNotFound
	}
	return state.ReadFile(bytes.NewReader(cloneBytes(file.Data)))
}

// DeleteFile removes a stored file. Missing keys return nil.
func (a *Artifacts) DeleteFile(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	inj := a.faultInjector()
	if err := inj.maybeArtifactErr(); err != nil {
		return err
	}
	a.mu.Lock()
	_, existed := a.files[key]
	if existed {
		delete(a.files, key)
	}
	a.mu.Unlock()
	if existed {
		if err := inj.maybeCommitThenErr(); err != nil {
			return err
		}
	}
	return nil
}

// Close is a no-op for the in-memory artifact store.
func (a *Artifacts) Close() error { return nil }

// ComposeStore composes a user-scoped state.ConversationStore over a shared
// Backend and Artifacts store using records.New (preserving SequenceReader and
// Sequencer while keeping b's lifecycle owned by the caller) and state.Compose.
func ComposeStore(b *Backend, a *Artifacts, user users.User, taskBudgetUSD float64) state.ConversationStore {
	if a == nil {
		a = NewArtifacts()
	}
	return state.Compose(records.New(sharedBackend{b}, user, taskBudgetUSD), a)
}

type sharedBackend struct {
	*Backend
}

func (sharedBackend) Close() error { return nil }
