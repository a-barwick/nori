package app

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nori/internal/api"
	"nori/internal/computer"
	"nori/internal/connections"
	"nori/internal/homeai"
	"nori/internal/observability"
	"nori/internal/platform"
	"nori/internal/push"
	"nori/internal/settings"
	"nori/internal/state/docstore"
	"nori/internal/users"
	"nori/internal/wake"
)

const (
	DefaultIdleTTL       = 10 * time.Minute
	DefaultMaxCachedApps = 256
)

type Service struct {
	Users         users.Directory
	IdleTTL       time.Duration
	MaxCachedApps int
	Now           func() time.Time
	ConfigureApp  func(users.User, *App)

	home          *homeai.Service
	computer      *computer.Manager
	closeComputer func()
	pushStore     push.Store
	pushWorker    *push.OutboxWorker
	storage       *platform.Storage
	cfg           settings.Settings
	waker         wake.Waker
	mu            sync.Mutex
	apps          map[string]*userApp
	opening       map[string]chan struct{}
	collapsed     map[string]bool
	closed        bool
	refs          int
	closeOnce     sync.Once
}

type userApp struct {
	row      users.User
	app      *App
	refs     int
	retired  bool
	lastUsed time.Time
}

func OpenService(ctx context.Context, waker wake.Waker, cfg settings.Settings) (*Service, error) {
	if err := validateStartup(cfg); err != nil {
		return nil, err
	}
	storage, err := platform.OpenStorage(ctx, cfg)
	if err != nil {
		return nil, err
	}
	service := OpenServiceWithStorage(storage, waker, cfg)
	if cfg.HomeAI.Enabled() {
		service.home, err = openHomeAI(ctx, cfg)
		if err != nil {
			service.Close()
			return nil, err
		}
	}
	if cfg.Computer.Enabled() {
		service.computer, service.closeComputer, err = platform.OpenComputer(ctx, cfg, storage)
		if err != nil {
			service.Close()
			return nil, err
		}
	}
	return service, nil
}

func OpenServiceWithStorage(storage *platform.Storage, waker wake.Waker, cfg settings.Settings) *Service {
	if waker == nil && storage != nil {
		waker = platform.Waker(storage.Leases())
	}
	var dir users.Directory
	var pushStore push.Store
	if storage != nil {
		dir = storage.Users()
		if pp, ok := storage.Backend().(interface{ Pool() *pgxpool.Pool }); ok && pp.Pool() != nil {
			pushStore = push.NewPostgresStore(pp.Pool())
		}
	}
	if pushStore == nil {
		pushStore = push.NewMemoryStore()
	}
	var pushSender push.Sender
	if apnsCfg, ok := push.ConfigFromEnv(); ok {
		apnsCfg.Devices = pushStore
		if client, err := push.NewAPNSClient(apnsCfg); err == nil {
			pushSender = client
		}
	}
	pushWorker := &push.OutboxWorker{
		Store:  pushStore,
		Sender: pushSender,
	}
	return &Service{
		Users:         dir,
		IdleTTL:       DefaultIdleTTL,
		MaxCachedApps: DefaultMaxCachedApps,
		pushStore:     pushStore,
		pushWorker:    pushWorker,
		storage:       storage,
		cfg:           cfg,
		waker:         waker,
		apps:          map[string]*userApp{},
		opening:       map[string]chan struct{}{},
		collapsed:     map[string]bool{},
	}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) idleTTL() time.Duration {
	if s.IdleTTL > 0 {
		return s.IdleTTL
	}
	return DefaultIdleTTL
}

func (s *Service) maxCachedApps() int {
	if s.MaxCachedApps > 0 {
		return s.MaxCachedApps
	}
	return DefaultMaxCachedApps
}

func (s *Service) Backend() docstore.Backend {
	if s == nil || s.storage == nil {
		return nil
	}
	return s.storage.Backend()
}

func (s *Service) HomeRelayHandler() http.Handler {
	if s == nil || s.home == nil {
		return nil
	}
	return s.home.Handler()
}

func (s *Service) ComputerManager() *computer.Manager {
	if s == nil {
		return nil
	}
	return s.computer
}

func (s *Service) SetComputerManager(mgr *computer.Manager) {
	if s == nil {
		return
	}
	s.computer = mgr
}

func (s *Service) AdvisoryLocker() docstore.AdvisoryLocker {
	locker, _ := s.Backend().(docstore.AdvisoryLocker)
	return locker
}

func (s *Service) Ping(ctx context.Context) error {
	if pinger, ok := s.Backend().(docstore.Pinger); ok {
		return pinger.Ping(ctx)
	}
	return nil
}

func (s *Service) Leases() docstore.LeaseStore {
	if s == nil || s.storage == nil {
		return nil
	}
	return s.storage.Leases()
}

func (s *Service) WakeListener() docstore.WakeListener {
	if s == nil || s.storage == nil {
		return nil
	}
	return s.storage.WakeListener()
}

func (s *Service) closeShared() {
	s.closeOnce.Do(func() {
		if s.closeComputer != nil {
			s.closeComputer()
		}
		if s.home != nil {
			s.home.Close()
		}
		if s.Users != nil {
			_ = s.Users.Close()
		}
		if s.storage != nil {
			_ = s.storage.Close()
		}
	})
}

func (s *Service) evictLocked(now time.Time) []*App {
	ttl := s.idleTTL()
	var toClose []*App
	for id, entry := range s.apps {
		if entry.refs == 0 && now.Sub(entry.lastUsed) > ttl {
			entry.retired = true
			delete(s.apps, id)
			toClose = append(toClose, entry.app)
		}
	}
	maxCap := s.maxCachedApps()
	if len(s.apps) <= maxCap {
		return toClose
	}
	type candidate struct {
		id       string
		entry    *userApp
		lastUsed time.Time
	}
	var idle []candidate
	for id, entry := range s.apps {
		if entry.refs == 0 {
			idle = append(idle, candidate{id: id, entry: entry, lastUsed: entry.lastUsed})
		}
	}
	sort.Slice(idle, func(i, j int) bool {
		if !idle[i].lastUsed.Equal(idle[j].lastUsed) {
			return idle[i].lastUsed.Before(idle[j].lastUsed)
		}
		return idle[i].id < idle[j].id
	})
	excess := len(s.apps) - maxCap
	for i := 0; i < len(idle) && i < excess; i++ {
		c := idle[i]
		c.entry.retired = true
		delete(s.apps, c.id)
		toClose = append(toClose, c.entry.app)
	}
	return toClose
}

func (s *Service) EvictIdle() int {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0
	}
	toClose := s.evictLocked(s.now())
	s.mu.Unlock()
	for _, a := range toClose {
		a.Close()
	}
	return len(toClose)
}

func (s *Service) CachedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.apps)
}

func (s *Service) releaseEntry(entry *userApp) func() {
	return s.releaseEntryWithTouch(entry, true)
}

func (s *Service) releaseEntryWithTouch(entry *userApp, touch bool) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			var toClose []*App
			var closeShared bool
			now := s.now()
			s.mu.Lock()
			entry.refs--
			if touch {
				entry.lastUsed = now
			}
			if entry.retired && entry.refs == 0 {
				toClose = append(toClose, entry.app)
			}
			if touch && !s.closed && entry.refs == 0 {
				toClose = append(toClose, s.evictLocked(now)...)
			}
			s.refs--
			if s.closed && s.refs == 0 {
				closeShared = true
			}
			s.mu.Unlock()
			for _, a := range toClose {
				a.Close()
			}
			if closeShared {
				s.closeShared()
			}
		})
	}
}

func (s *Service) Acquire(ctx context.Context, row users.User) (*App, func(), error) {
	return s.acquire(ctx, row)
}

func (s *Service) AcquireByID(ctx context.Context, userID string) (*App, func(), error) {
	if s.Users == nil {
		return nil, nil, errors.New("user directory is not configured")
	}
	row, err := s.Users.Get(ctx, userID)
	if err != nil {
		return nil, nil, err
	}
	if row.ResetPending {
		return nil, nil, docstore.ErrUserDataResetPending
	}
	if pending, err := s.isAccountDeletePending(ctx, row.ID); err != nil {
		return nil, nil, err
	} else if pending {
		return nil, nil, api.ErrAccountDeletePending
	}
	return s.acquire(ctx, row)
}

func (s *Service) RepairUser(ctx context.Context, row users.User) error {
	ctx, row, done, err := s.userDataActivity(ctx, row)
	if err != nil {
		if errors.Is(err, docstore.ErrUserDataBusy) {
			return nil
		}
		return err
	}
	defer done()
	if row.ResetPending || isAccountDeletePendingCtx(ctx) {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("service closed")
	}
	if entry := s.apps[row.ID]; entry != nil && entry.row == row {
		entry.refs++
		s.refs++
		s.mu.Unlock()
		release := s.releaseEntryWithTouch(entry, false)
		defer release()
		return entry.app.RepairLeases(ctx)
	}
	s.refs++
	s.mu.Unlock()

	defer func() {
		var closeShared bool
		s.mu.Lock()
		s.refs--
		if s.closed && s.refs == 0 {
			closeShared = true
		}
		s.mu.Unlock()
		if closeShared {
			s.closeShared()
		}
	}()

	opened, _, err := openWithResources(ctx, s.waker, s.cfg, row, s.storage, openOptions{
		skipCollapseDuplicates: true,
		home:                   s.home,
		computer:               s.computer,
		pushStore:              s.pushStore,
		pushWorker:             s.pushWorker,
	})
	if err != nil {
		return err
	}
	defer opened.Close()
	if s.ConfigureApp != nil {
		s.ConfigureApp(row, opened)
	}
	return opened.RepairLeases(ctx)
}

func (s *Service) acquire(ctx context.Context, row users.User) (*App, func(), error) {
	for {
		now := s.now()
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, nil, errors.New("service closed")
		}
		if entry := s.apps[row.ID]; entry != nil && entry.row == row {
			entry.refs++
			entry.lastUsed = now
			s.refs++
			s.mu.Unlock()
			return entry.app, s.releaseEntry(entry), nil
		}
		if wait := s.opening[row.ID]; wait != nil {
			s.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		if s.opening == nil {
			s.opening = map[string]chan struct{}{}
		}
		done := make(chan struct{})
		s.opening[row.ID] = done
		alreadyCollapsed := s.collapsed[row.ID]
		s.refs++
		s.mu.Unlock()

		opened, err := s.openUserApp(ctx, row, done, alreadyCollapsed)
		if err != nil {
			return nil, nil, err
		}
		return opened.app, s.releaseEntry(opened), nil
	}
}

func (s *Service) openUserApp(ctx context.Context, row users.User, done chan struct{}, alreadyCollapsed bool) (entry *userApp, err error) {
	var committed bool
	defer func() {
		if committed {
			return
		}
		var closeShared bool
		s.mu.Lock()
		delete(s.opening, row.ID)
		close(done)
		s.refs--
		if s.closed && s.refs == 0 {
			closeShared = true
		}
		s.mu.Unlock()
		if closeShared {
			s.closeShared()
		}
	}()
	opened, collapsed, err := openWithResources(ctx, s.waker, s.cfg, row, s.storage, openOptions{
		skipCollapseDuplicates: alreadyCollapsed || row.ResetPending,
		home:                   s.home,
		computer:               s.computer,
		pushStore:              s.pushStore,
		pushWorker:             s.pushWorker,
	})
	if err != nil {
		return nil, err
	}
	if s.ConfigureApp != nil {
		s.ConfigureApp(row, opened)
	}
	if opened.Assistant != nil {
		opened.Assistant.DeleteAccount = func(ctx context.Context, confirmation string, resumePending bool) error {
			return s.deleteUserAccount(ctx, row, confirmation, resumePending)
		}
	}
	opened.guardWakeRepair = func(ctx context.Context) (func(), error) {
		ctx, current, done, err := s.userDataActivity(ctx, row)
		if err != nil {
			return nil, err
		}
		if isAccountDeletePendingCtx(ctx) {
			done()
			return nil, api.ErrAccountDeletePending
		}
		if current.ResetPending || current.DataResetAt != row.DataResetAt {
			done()
			return nil, docstore.ErrUserDataResetPending
		}
		return done, nil
	}
	opened.handler = wrapAccountDeleteGuard(api.Handler(opened.apiDeps()))
	var toClose []*App
	var closeShared bool
	now := s.now()
	s.mu.Lock()
	if collapsed {
		if s.collapsed == nil {
			s.collapsed = map[string]bool{}
		}
		s.collapsed[row.ID] = true
	}
	delete(s.opening, row.ID)
	close(done)
	committed = true
	if s.closed {
		s.refs--
		if s.refs == 0 {
			closeShared = true
		}
		s.mu.Unlock()
		opened.Close()
		if closeShared {
			s.closeShared()
		}
		return nil, errors.New("service closed")
	}
	if old := s.apps[row.ID]; old != nil {
		old.retired = true
		if old.refs == 0 {
			toClose = append(toClose, old.app)
		}
	}
	entry = &userApp{row: row, app: opened, refs: 1, lastUsed: now}
	s.apps[row.ID] = entry
	toClose = append(toClose, s.evictLocked(now)...)
	s.mu.Unlock()
	for _, a := range toClose {
		a.Close()
	}
	return entry, nil
}

func (s *Service) Handler(local bool) http.Handler {
	var googleOAuthUser func(string) string
	if key := s.cfg.Connections.GoogleWorkspace.ClientSecret; key != "" {
		googleOAuthUser = func(raw string) string {
			return connections.GoogleWorkspaceCallbackUser(raw, key, s.now())
		}
	}
	return wrapAccountDeleteRequest(api.UserHandlerWithOptions(s.Users, func(ctx context.Context, row users.User) (api.UserRoutes, func(), error) {
		userApp, release, err := s.acquire(ctx, row)
		if err != nil {
			return api.UserRoutes{}, nil, err
		}
		resume := func(ctx context.Context) error {
			if isAccountDeletePendingCtx(ctx) {
				return nil
			}
			return userApp.Resume(ctx)
		}
		return api.UserRoutes{Handler: userApp.handler, Resume: resume}, release, nil
	}, local, s.cfg.Models.Outer, api.UserHandlerOptions{
		ReadyCheck:      s.Ping,
		Metrics:         observability.MetricsHandler(),
		UserActivity:    s.userDataActivity,
		ResetData:       s.resetUserData,
		GoogleOAuthUser: googleOAuthUser,
	}))
}

func (s *Service) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	var toClose []*App
	for id, entry := range s.apps {
		entry.retired = true
		if entry.refs == 0 {
			toClose = append(toClose, entry.app)
		}
		delete(s.apps, id)
	}
	closeShared := s.refs == 0
	s.mu.Unlock()
	for _, a := range toClose {
		a.Close()
	}
	if closeShared {
		s.closeShared()
	}
}
