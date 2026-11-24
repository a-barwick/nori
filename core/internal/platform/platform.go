// Package platform wires processes to their configured infrastructure. Nothing
// here has a default target: a process with missing configuration fails at
// startup instead of reaching a deployed project.
//
// This is the only production code that names a storage backend. Everything
// else depends on the contracts in internal/state.
package platform

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"sync"
	"time"

	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/localfs"
	"nori/internal/state/postgres"
	"nori/internal/state/records"
	"nori/internal/state/s3"
	"nori/internal/usage"
	"nori/internal/users"
)

// Storage holds one process-wide document backend and S3 object client so
// per-user scopes share connection pools instead of opening one per user.
type Storage struct {
	docs      docstore.Backend
	artifacts func(users.User) state.ArtifactStore
	// Now, when set, overrides the clock used by opened records.Database stores (used in simulation tests).
	Now func() time.Time
	mu  sync.Mutex
	s3  *s3.Client
}

func OpenStorage(ctx context.Context, s settings.Settings) (*Storage, error) {
	docs, err := openDocBackend(ctx, s.Store)
	if err != nil {
		return nil, err
	}
	return &Storage{docs: docs}, nil
}

// NewStorage wraps an already-open document backend and optional per-user artifact factory.
func NewStorage(docs docstore.Backend, artifacts func(users.User) state.ArtifactStore) *Storage {
	return &Storage{docs: docs, artifacts: artifacts}
}

func (p *Storage) Backend() docstore.Backend {
	if p == nil {
		return nil
	}
	return p.docs
}

func (p *Storage) Users() users.Directory {
	return records.NewSharedUsers(p.docs)
}

func (p *Storage) Leases() docstore.LeaseStore {
	if p == nil {
		return nil
	}
	leases, _ := p.docs.(docstore.LeaseStore)
	return leases
}

func (p *Storage) WakeListener() docstore.WakeListener {
	if p == nil {
		return nil
	}
	listener, _ := p.docs.(docstore.WakeListener)
	return listener
}

func (p *Storage) OpenStore(ctx context.Context, s settings.Settings, user users.User) (state.ConversationStore, error) {
	if err := user.Validate(); err != nil {
		return nil, err
	}
	documents := records.NewShared(p.docs, user, s.TaskBudgetUSD)
	if p.Now != nil {
		documents.Now = p.Now
	}
	if p.artifacts != nil {
		return meteredStore(s, user, state.Compose(documents, p.artifacts(user)))
	}
	local, err := openLocalArtifacts(s, user)
	if err != nil {
		_ = documents.Close()
		return nil, err
	}
	if local != nil {
		return meteredStore(s, user, state.Compose(documents, local))
	}
	objects, err := p.openObjects(ctx, s, user)
	if err != nil {
		_ = documents.Close()
		return nil, err
	}
	return meteredStore(s, user, state.Compose(documents, objects))
}

func (p *Storage) openObjects(ctx context.Context, s settings.Settings, user users.User) (state.ArtifactStore, error) {
	if s.Cloud != "aws" {
		return nil, fmt.Errorf("object storage requires NORI_CLOUD=aws (or NORI_LOCAL_ARTIFACT_DIR on NORI_CLOUD=local)")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.s3 == nil {
		c, err := s3.OpenClient(ctx, s.AWS.Region)
		if err != nil {
			return nil, err
		}
		p.s3 = c
	}
	return p.s3.ForUser(s.Store.ArtifactBucket, user)
}

func (p *Storage) Close() error {
	return p.docs.Close()
}

func openDocBackend(ctx context.Context, cfg settings.Store) (docstore.Backend, error) {
	switch cfg.Backend {
	case "", "postgres":
		return postgres.OpenBackendWithOptions(ctx, cfg.PostgresURL, postgres.Options{
			ListenURL:         cfg.PostgresListenURL,
			MaxConns:          cfg.PostgresMaxConns,
			MinConns:          cfg.PostgresMinConns,
			MaxConnLifetime:   cfg.PostgresMaxConnLifetime,
			MaxConnIdleTime:   cfg.PostgresMaxConnIdleTime,
			HealthCheckPeriod: cfg.PostgresHealthCheckPeriod,
			StatementTimeout:  cfg.PostgresStatementTimeout,
		})
	default:
		return nil, fmt.Errorf("NORI_STORE_BACKEND must be postgres")
	}
}

func OpenStore(ctx context.Context, s settings.Settings, user users.User) (state.ConversationStore, error) {
	if err := user.Validate(); err != nil {
		return nil, err
	}
	local, err := openLocalArtifacts(s, user)
	if err != nil {
		return nil, err
	}
	closeLocal := func() {
		if local != nil {
			_ = local.Close()
		}
	}
	docs, err := openDocBackend(ctx, s.Store)
	if err != nil {
		closeLocal()
		return nil, err
	}
	documents := records.New(docs, user, s.TaskBudgetUSD)
	if local != nil {
		return meteredStore(s, user, state.Compose(documents, local))
	}
	if s.Cloud != "aws" {
		_ = documents.Close()
		return nil, fmt.Errorf("object storage requires NORI_CLOUD=aws (or NORI_LOCAL_ARTIFACT_DIR on NORI_CLOUD=local)")
	}
	objects, err := s3.Open(ctx, s.AWS.Region, s.Store.ArtifactBucket, user)
	if err != nil {
		_ = documents.Close()
		return nil, err
	}
	return meteredStore(s, user, state.Compose(documents, objects))
}

// openLocalArtifacts opens the development artifact directory when NORI_LOCAL_ARTIFACT_DIR is set. It returns nil
// when it is not set. The directory is allowed only with a loopback database.
func openLocalArtifacts(s settings.Settings, user users.User) (state.ArtifactStore, error) {
	directory := s.Store.LocalArtifactDir
	if directory == "" {
		if s.Cloud == "local" {
			return nil, fmt.Errorf("NORI_CLOUD=local requires NORI_LOCAL_ARTIFACT_DIR")
		}
		return nil, nil
	}
	u, e := url.Parse(s.Store.PostgresURL)
	localDatabase := e == nil && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback())
	if !localDatabase {
		return nil, fmt.Errorf("NORI_LOCAL_ARTIFACT_DIR requires a loopback Postgres database")
	}
	store, err := localfs.Open(filepath.Join(directory, user.FilePrefix()))
	if err != nil {
		return nil, fmt.Errorf("NORI_LOCAL_ARTIFACT_DIR %w", err)
	}
	return store, nil
}

func meteredStore(s settings.Settings, user users.User, store state.ConversationStore) (state.ConversationStore, error) {
	meter, err := usage.Open(s.Metering, user.ID, store)
	if err != nil {
		store.Close()
		return nil, err
	}
	return usage.Wrap(store, meter), nil
}
