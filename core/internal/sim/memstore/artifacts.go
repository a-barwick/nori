package memstore

import (
	"bytes"
	"context"
	"sync"

	"nori/internal/state"
	"nori/internal/state/records"
	"nori/internal/users"
)

type StoredFile struct {
	MediaType string
	Data      []byte
}

type ArtifactOption func(*Artifacts)

func WithArtifactsFaultInjector(inj *FaultInjector) ArtifactOption {
	return func(a *Artifacts) {
		a.faults = inj
	}
}

func WithArtifactsFaults(cfg FaultConfig, rng Float64Source) ArtifactOption {
	return func(a *Artifacts) {
		a.faults = NewFaultInjector(cfg, rng)
	}
}

type Artifacts struct {
	mu     sync.Mutex
	pngs   map[string][]byte
	files  map[string]StoredFile
	faults *FaultInjector
}

var (
	_ state.Artifacts     = (*Artifacts)(nil)
	_ state.ArtifactStore = (*Artifacts)(nil)
)

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

func (a *Artifacts) SetFaults(cfg FaultConfig, rng Float64Source) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.faults == nil {
		a.faults = NewFaultInjector(cfg, rng)
		return
	}
	a.faults.Configure(cfg, rng)
}

func (a *Artifacts) SetFaultsEnabled(enabled bool) {
	a.mu.Lock()
	inj := a.faults
	a.mu.Unlock()
	if inj != nil {
		inj.SetEnabled(enabled)
	}
}

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

func (a *Artifacts) Close() error { return nil }

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
