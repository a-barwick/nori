package platform

import (
	"context"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5/pgxpool"
	"nori/internal/computer"
	"nori/internal/computer/docker"
	"nori/internal/computer/fake"
	"nori/internal/computer/sprites"
	"nori/internal/settings"
)

type pgPoolProvider interface {
	Pool() *pgxpool.Pool
}

// OpenComputer constructs the Personal Agent Computer Manager according to settings.
// Returns (nil, nil, nil) when NORI_COMPUTER_PROVIDER is disabled.
func OpenComputer(ctx context.Context, s settings.Settings, storage *Storage) (*computer.Manager, func(), error) {
	if !s.Computer.Enabled() {
		return nil, nil, nil
	}

	var provider computer.Provider
	var closers []func()

	switch s.Computer.Provider {
	case "sprites":
		sp, err := sprites.New(sprites.Config{
			Token:     s.Computer.SpritesAPIToken,
			Org:       s.Computer.SpritesOrg,
			BaseURL:   s.Computer.SpritesBaseURL,
			StorageGB: s.Computer.MaxDiskGB,
		})
		if err != nil {
			return nil, nil, err
		}
		provider = sp
		closers = append(closers, func() { _ = sp.Close() })
	case "docker":
		provider = docker.New(docker.Config{
			Image:         s.Computer.DockerImage,
			LocalhostOnly: s.Computer.DockerLocalhostOnly,
			MemoryLimitMB: s.Computer.MemoryWatchdogMB,
		})
	case "fake":
		provider = fake.New()
	default:
		return nil, nil, fmt.Errorf("unsupported NORI_COMPUTER_PROVIDER %q", s.Computer.Provider)
	}

	var store computer.Store
	if storage != nil {
		if pp, ok := storage.Backend().(pgPoolProvider); ok && pp.Pool() != nil {
			store = computer.NewPostgresStore(pp.Pool())
		}
	}
	if store == nil && s.Store.PostgresURL != "" && s.Computer.Provider != "fake" {
		ps, err := computer.OpenPostgresStore(ctx, s.Store.PostgresURL, 4)
		if err != nil {
			for _, fn := range closers {
				fn()
			}
			return nil, nil, err
		}
		store = ps
		closers = append(closers, ps.Close)
	}
	if store == nil {
		store = computer.NewMemoryStore()
	}

	mgr := computer.NewManager(provider, store, computer.Config{
		MemoryLimitMB: s.Computer.MemoryWatchdogMB,
		MaxDiskGB:     s.Computer.MaxDiskGB,
	})
	if c, ok := provider.(io.Closer); ok && len(closers) == 0 {
		closers = append(closers, func() { _ = c.Close() })
	}
	cleanup := func() {
		for _, fn := range closers {
			fn()
		}
	}
	return mgr, cleanup, nil
}
