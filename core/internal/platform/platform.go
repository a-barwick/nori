package platform

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"sync"
	"time"

	"nori/internal/modelprofiles"
	"nori/internal/secret/tenantkey"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/state/encrypted"
	"nori/internal/state/gcs"
	"nori/internal/state/localfs"
	"nori/internal/state/postgres"
	"nori/internal/state/records"
	"nori/internal/state/s3"
	"nori/internal/usage"
	"nori/internal/users"
)

type Storage struct {
	docs      docstore.Backend
	artifacts func(users.User) state.ArtifactStore
	Now       func() time.Time
	mu        sync.Mutex
	s3        *s3.Client
	gcs       *gcs.Client
	dekCache  *tenantkey.Cache
}

func OpenStorage(ctx context.Context, s settings.Settings) (*Storage, error) {
	docs, err := openDocBackend(ctx, s.Store)
	if err != nil {
		return nil, err
	}
	return &Storage{
		docs:     docs,
		dekCache: tenantkey.NewCache(tenantkey.DefaultCacheTTL),
	}, nil
}

func NewStorage(docs docstore.Backend, artifacts func(users.User) state.ArtifactStore) *Storage {
	return &Storage{
		docs:      docs,
		artifacts: artifacts,
		dekCache:  tenantkey.NewCache(tenantkey.DefaultCacheTTL),
	}
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
	if s.Models.BYOKRequired {
		documents.PinAccountModelSelection = modelprofiles.PinAccountSelection
	}
	if p.Now != nil {
		documents.Now = p.Now
	}
	if p.artifacts != nil {
		artifacts, err := p.wrapUserArtifacts(s, user, documents, p.artifacts(user))
		if err != nil {
			_ = documents.Close()
			return nil, err
		}
		return meteredStore(s, user, state.Compose(documents, artifacts))
	}
	local, err := openLocalArtifacts(s, user)
	if err != nil {
		_ = documents.Close()
		return nil, err
	}
	if local != nil {
		artifacts, err := p.wrapUserArtifacts(s, user, documents, local)
		if err != nil {
			_ = local.Close()
			_ = documents.Close()
			return nil, err
		}
		return meteredStore(s, user, state.Compose(documents, artifacts))
	}
	objects, err := p.openObjects(ctx, s, user)
	if err != nil {
		_ = documents.Close()
		return nil, err
	}
	artifacts, err := p.wrapUserArtifacts(s, user, documents, objects)
	if err != nil {
		_ = objects.Close()
		_ = documents.Close()
		return nil, err
	}
	return meteredStore(s, user, state.Compose(documents, artifacts))
}

func (p *Storage) getDEKCache() *tenantkey.Cache {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dekCache == nil {
		p.dekCache = tenantkey.NewCache(tenantkey.DefaultCacheTTL)
	}
	return p.dekCache
}

func userArtifactKeyScope(user users.User) string {
	if user.DataResetAt == "" {
		return user.ID
	}
	return user.ID + "/" + user.DataResetAt
}

func (p *Storage) EvictUserArtifactKey(user users.User) {
	p.getDEKCache().Evict(userArtifactKeyScope(user))
}

func (p *Storage) wrapUserArtifacts(s settings.Settings, user users.User, resources state.ResourceStore, inner state.ArtifactStore) (state.ArtifactStore, error) {
	if s.Connections.CredentialStore != "database" {
		return inner, nil
	}
	keys, err := credentialKeys(s)
	if err != nil {
		return nil, err
	}
	mgr, err := tenantkey.NewManager(resources, keys, user.ID)
	if err != nil {
		return nil, err
	}
	mgr.Cache = p.getDEKCache()
	mgr.CacheScope = userArtifactKeyScope(user)
	if p.Now != nil {
		mgr.Now = p.Now
	}
	return encrypted.NewArtifactStore(inner, mgr)
}

func (p *Storage) openObjects(ctx context.Context, s settings.Settings, user users.User) (state.ArtifactStore, error) {
	switch s.Cloud {
	case "aws":
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
	case "gcp":
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.gcs == nil {
			c, err := gcs.OpenClient(ctx)
			if err != nil {
				return nil, err
			}
			p.gcs = c
		}
		return p.gcs.ForUser(s.Store.ArtifactBucket, user)
	default:
		return nil, fmt.Errorf("object storage requires NORI_CLOUD=aws or NORI_CLOUD=gcp (or NORI_LOCAL_ARTIFACT_DIR on NORI_CLOUD=local)")
	}
}

func (p *Storage) Close() error {
	p.mu.Lock()
	cache := p.dekCache
	p.mu.Unlock()
	if cache != nil {
		cache.Close()
	}
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
	if s.Models.BYOKRequired {
		documents.PinAccountModelSelection = modelprofiles.PinAccountSelection
	}
	if local != nil {
		return meteredStore(s, user, state.Compose(documents, local))
	}
	var objects state.ArtifactStore
	switch s.Cloud {
	case "aws":
		objects, err = s3.Open(ctx, s.AWS.Region, s.Store.ArtifactBucket, user)
	case "gcp":
		objects, err = gcs.Open(ctx, s.Store.ArtifactBucket, user)
	default:
		_ = documents.Close()
		return nil, fmt.Errorf("object storage requires NORI_CLOUD=aws or NORI_CLOUD=gcp (or NORI_LOCAL_ARTIFACT_DIR on NORI_CLOUD=local)")
	}
	if err != nil {
		_ = documents.Close()
		return nil, err
	}
	return meteredStore(s, user, state.Compose(documents, objects))
}

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
