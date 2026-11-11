package app

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"nori/internal/api"
	"nori/internal/platform"
	"nori/internal/settings"
	"nori/internal/users"
	"nori/internal/wake"
)

// Service owns shared process resources and immutable per-user applications.
// A changed row takes effect on the next request or processing pass; an in-flight
// pass keeps its own scope until it finishes.
type Service struct {
	Users users.Directory
	cfg   settings.Settings
	waker wake.Waker
	mu    sync.Mutex
	apps  map[string]*userApp
}
type userApp struct {
	row     users.User
	app     *App
	refs    int
	retired bool
}

func OpenService(ctx context.Context, waker wake.Waker, cfg settings.Settings) (*Service, error) {
	if err := validateStartup(cfg); err != nil {
		return nil, err
	}
	directory, err := platform.OpenUsers(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Service{Users: directory, cfg: cfg, waker: waker, apps: map[string]*userApp{}}, nil
}
func (s *Service) acquire(ctx context.Context, row users.User) (*App, func(), error) {
	s.mu.Lock()
	entry := s.apps[row.ID]
	if entry == nil || entry.row != row {
		app, err := Open(ctx, s.waker, s.cfg, row)
		if err != nil {
			s.mu.Unlock()
			return nil, nil, err
		}
		if entry != nil {
			entry.retired = true
			if entry.refs == 0 {
				entry.app.Close()
			}
		}
		app.handler = api.Handler(app.apiDeps())
		entry = &userApp{row: row, app: app}
		s.apps[row.ID] = entry
	}
	entry.refs++
	s.mu.Unlock()
	return entry.app, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		entry.refs--
		if entry.retired && entry.refs == 0 {
			entry.app.Close()
		}
	}, nil
}
func (s *Service) Handler(local bool) http.Handler {
	return api.UserHandler(s.Users, func(ctx context.Context, row users.User) (api.UserRoutes, func(), error) {
		app, release, err := s.acquire(ctx, row)
		if err != nil {
			return api.UserRoutes{}, nil, err
		}
		return api.UserRoutes{Handler: app.handler, Resume: app.Resume}, release, nil
	}, local, s.cfg.Models.Outer)
}
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.apps {
		entry.retired = true
		if entry.refs == 0 {
			entry.app.Close()
		}
	}
	_ = s.Users.Close()
}

// LocalLoop discovers inserted rows and recovers each user's work independently.
func (s *Service) LocalLoop(ctx context.Context, notifications <-chan struct{}) {
	var wg sync.WaitGroup
	defer wg.Wait()
	var mu sync.Mutex
	running := map[string]bool{}
	// A long assistant pass must not delay foreground chat recovery.
	processors := []struct {
		name string
		run  func(*App, context.Context) error
	}{
		{"assistant", (*App).processAssistant},
		{"chats", func(a *App, ctx context.Context) error { return a.Chats.Process(ctx) }},
		{"scheduler", (*App).Schedule},
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		rows, err := s.Users.List(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("user recovery: %v", err)
		}
		for _, row := range rows {
			for _, processor := range processors {
				key := row.ID + "/" + processor.name
				mu.Lock()
				if running[key] {
					mu.Unlock()
					continue
				}
				running[key] = true
				mu.Unlock()
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer func() { mu.Lock(); delete(running, key); mu.Unlock() }()
					pass, cancel := context.WithTimeout(ctx, 9*time.Minute)
					defer cancel()
					app, release, err := s.acquire(pass, row)
					if err == nil {
						defer release()
						err = processor.run(app, pass)
					}
					if err != nil && ctx.Err() == nil && !errors.Is(err, context.Canceled) {
						log.Printf("user %s processing will retry: %v", row.ID, err)
					}
				}()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-notifications:
		case <-ticker.C:
		}
	}
}
