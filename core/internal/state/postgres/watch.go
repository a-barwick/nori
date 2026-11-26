package postgres

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nori/internal/state/docstore"
)

const (
	watchChannel                 = "nori_watch"
	defaultWatchFallbackInterval = 20 * time.Second
	defaultWatchReadyTimeout     = 500 * time.Millisecond
)

var (
	watchFallbackInterval = defaultWatchFallbackInterval
	watchHubIdleGrace     = 5 * time.Second
	watchReadyTimeout     = defaultWatchReadyTimeout
)

type watchSub struct {
	collection string
	ch         chan struct{}
}

type watchHub struct {
	b              *backend
	closed         bool
	subs           map[string]map[*watchSub]struct{}
	total          int64
	active         atomic.Int64
	running        bool
	listenReady    bool
	needsBroadcast bool
	gen            uint64
	ready          chan struct{}
	cancel         context.CancelFunc
	idleTimer      *time.Timer
	wg             sync.WaitGroup
	limiter        *logLimiter
}

func newWatchHub(b *backend) *watchHub {
	return &watchHub{
		b:       b,
		subs:    make(map[string]map[*watchSub]struct{}),
		limiter: newLogLimiter(logRateLimitInterval),
	}
}

func (b *backend) listenEndpoint() string {
	if b == nil {
		return ""
	}
	if u := strings.TrimSpace(b.listenURL); u != "" {
		return u
	}
	return strings.TrimSpace(b.connURL)
}

func (b *backend) closeWatchHub() {
	if b == nil {
		return
	}
	b.watchMu.Lock()
	h := b.hub
	if h == nil {
		b.hub = &watchHub{b: b, closed: true}
		b.watchMu.Unlock()
		return
	}
	if h.closed {
		b.watchMu.Unlock()
		h.wg.Wait()
		return
	}
	h.closed = true
	if h.idleTimer != nil {
		h.idleTimer.Stop()
		h.idleTimer = nil
	}
	cancel := h.cancel
	h.cancel = nil
	h.running = false
	h.listenReady = false
	h.ready = nil
	b.watchMu.Unlock()

	if cancel != nil {
		cancel()
	}
	h.wg.Wait()
}

func (b *backend) activeWatchers() int64 {
	if b == nil {
		return 0
	}
	b.watchMu.Lock()
	h := b.hub
	b.watchMu.Unlock()
	if h == nil {
		return 0
	}
	return h.active.Load()
}

func (b *backend) subscribeWatch(collection string) (*watchSub, <-chan struct{}, error) {
	b.watchMu.Lock()
	defer b.watchMu.Unlock()

	if b.hub == nil {
		b.hub = newWatchHub(b)
	}
	h := b.hub
	if h.closed {
		return nil, nil, errors.New("postgres backend is closed")
	}

	if h.idleTimer != nil {
		h.idleTimer.Stop()
		h.idleTimer = nil
	}

	sub := &watchSub{
		collection: collection,
		ch:         make(chan struct{}, 1),
	}
	set := h.subs[collection]
	if set == nil {
		set = make(map[*watchSub]struct{})
		h.subs[collection] = set
	}
	set[sub] = struct{}{}
	h.total++
	h.active.Store(h.total)

	if !h.running {
		url := b.listenEndpoint()
		if url != "" {
			runCtx, cancel := context.WithCancel(context.Background())
			ready := make(chan struct{})
			h.gen++
			gen := h.gen
			h.running = true
			h.listenReady = false
			h.ready = ready
			h.cancel = cancel
			h.wg.Add(1)
			go h.run(runCtx, url, gen, ready)
		}
	}
	return sub, h.ready, nil
}

func (b *backend) unsubscribeWatch(sub *watchSub) {
	if b == nil || sub == nil {
		return
	}
	var cancel context.CancelFunc
	b.watchMu.Lock()
	h := b.hub
	if h != nil {
		if set := h.subs[sub.collection]; set != nil {
			if _, ok := set[sub]; ok {
				delete(set, sub)
				if len(set) == 0 {
					delete(h.subs, sub.collection)
				}
				h.total--
				h.active.Store(h.total)
			}
		}
		if h.total == 0 && h.running && !h.closed {
			grace := watchHubIdleGrace
			if grace <= 0 {
				cancel = h.stopListenerLocked()
			} else {
				gen := h.gen
				if h.idleTimer != nil {
					h.idleTimer.Stop()
				}
				h.idleTimer = time.AfterFunc(grace, func() {
					b.onWatchIdleTimeout(gen)
				})
			}
		}
	}
	b.watchMu.Unlock()

	if cancel != nil {
		cancel()
	}
}

func (b *backend) onWatchIdleTimeout(gen uint64) {
	var cancel context.CancelFunc
	b.watchMu.Lock()
	h := b.hub
	if h != nil && !h.closed && h.running && h.total == 0 && h.gen == gen {
		cancel = h.stopListenerLocked()
	}
	b.watchMu.Unlock()

	if cancel != nil {
		cancel()
	}
}

func (h *watchHub) stopListenerLocked() context.CancelFunc {
	if h.idleTimer != nil {
		h.idleTimer.Stop()
		h.idleTimer = nil
	}
	cancel := h.cancel
	h.cancel = nil
	h.running = false
	h.listenReady = false
	h.ready = nil
	return cancel
}

func (b *backend) markWatchNeedsBroadcast() {
	b.watchMu.Lock()
	h := b.hub
	if h != nil && !h.closed && !h.listenReady {
		h.needsBroadcast = true
	}
	b.watchMu.Unlock()
}

func (h *watchHub) onConnected(runCtx context.Context, gen uint64, forceBroadcast bool, closeReady func()) {
	h.b.watchMu.Lock()
	defer h.b.watchMu.Unlock()

	if runCtx.Err() != nil || h.closed || !h.running || h.gen != gen {
		closeReady()
		return
	}
	h.listenReady = true
	if forceBroadcast || h.needsBroadcast {
		h.broadcastAllLocked()
		h.needsBroadcast = false
	}
	closeReady()
}

func (h *watchHub) onDisconnected(gen uint64) {
	h.b.watchMu.Lock()
	if h.gen == gen {
		h.listenReady = false
	}
	h.b.watchMu.Unlock()
}

func (h *watchHub) notifyCollection(collection string) {
	h.b.watchMu.Lock()
	for sub := range h.subs[collection] {
		select {
		case sub.ch <- struct{}{}:
		default:
		}
	}
	h.b.watchMu.Unlock()
}

func (h *watchHub) broadcastAllLocked() {
	for _, set := range h.subs {
		for sub := range set {
			select {
			case sub.ch <- struct{}{}:
			default:
			}
		}
	}
}

func (h *watchHub) run(runCtx context.Context, url string, gen uint64, ready chan struct{}) {
	defer h.wg.Done()
	var readyOnce sync.Once
	closeReady := func() {
		readyOnce.Do(func() { close(ready) })
	}
	defer closeReady()

	backoff := listenInitialBackoff
	connectedOnce := false
	hadError := false

	for {
		if runCtx.Err() != nil {
			return
		}
		conn, err := connectListen(runCtx, url)
		phase := "connect"
		if err == nil {
			phase = "listen"
			_, err = conn.Exec(runCtx, "LISTEN "+watchChannel)
			if err != nil {
				closeConn(conn)
			}
		}
		if err != nil {
			closeReady()
			if runCtx.Err() != nil {
				return
			}
			hadError = true
			h.b.watchErrors.Add(1)
			if emit, suppressed := h.limiter.allowWarn(time.Now()); emit {
				slog.WarnContext(runCtx, "postgres watch listener error",
					"channel", watchChannel,
					"phase", phase,
					"suppressed", suppressed,
					"err", err,
				)
			}
			if !sleepBackoff(runCtx, backoff) {
				return
			}
			backoff = min(backoff*2, listenMaxBackoff)
			continue
		}

		if connectedOnce {
			reconnects := h.b.watchReconnects.Add(1)
			if wasFailing, suppressed := h.limiter.recover(); wasFailing {
				slog.InfoContext(runCtx, "postgres watch listener recovered",
					"channel", watchChannel,
					"reconnects", reconnects,
					"suppressed_errors", suppressed,
				)
			}
		} else if wasFailing, suppressed := h.limiter.recover(); wasFailing {
			slog.InfoContext(runCtx, "postgres watch listener connected after errors",
				"channel", watchChannel,
				"suppressed_errors", suppressed,
			)
		}

		h.onConnected(runCtx, gen, connectedOnce || hadError, closeReady)
		connectedOnce = true
		hadError = false

		connectedAt := time.Now()
		for {
			notification, err := conn.WaitForNotification(runCtx)
			if err != nil {
				closeConn(conn)
				h.onDisconnected(gen)
				if runCtx.Err() != nil {
					return
				}
				hadError = true
				h.b.watchErrors.Add(1)
				if emit, suppressed := h.limiter.allowWarn(time.Now()); emit {
					slog.WarnContext(runCtx, "postgres watch listener error",
						"channel", watchChannel,
						"phase", "wait",
						"suppressed", suppressed,
						"err", err,
					)
				}
				break
			}
			backoff = listenInitialBackoff
			if notification != nil && notification.Payload != "" && runCtx.Err() == nil {
				h.notifyCollection(notification.Payload)
			}
		}
		if time.Since(connectedAt) >= listenMaxBackoff {
			backoff = listenInitialBackoff
		}
		if !sleepBackoff(runCtx, backoff) {
			return
		}
		backoff = min(backoff*2, listenMaxBackoff)
	}
}

func (b *backend) watchWakeEvents(collections []string) []docstore.WakeEvent {
	if len(collections) == 0 {
		return nil
	}
	events := make([]docstore.WakeEvent, 0, len(collections))
	for _, col := range collections {
		if col == "" {
			continue
		}
		events = append(events, docstore.WakeEvent{
			Channel: watchChannel,
			Payload: col,
		})
	}
	return events
}

func (b *backend) emitWatchCollections(ctx context.Context, collections []string) {
	if len(collections) == 0 {
		return
	}
	b.emitWakes(ctx, b.watchWakeEvents(collections))
}

func (b *backend) ListenStats() docstore.ListenStats {
	if b == nil {
		return docstore.ListenStats{}
	}
	return docstore.ListenStats{
		WakeReconnects:  b.wakeReconnects.Load(),
		WakeErrors:      b.wakeErrors.Load(),
		WatchReconnects: b.watchReconnects.Load(),
		WatchErrors:     b.watchErrors.Load(),
		ActiveWatchers:  b.activeWatchers(),
	}
}

func waitWatchReady(ctx context.Context, ready <-chan struct{}, onTimeout func()) error {
	if ready == nil {
		return nil
	}
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	timeout := watchReadyTimeout
	if timeout <= 0 {
		timeout = defaultWatchReadyTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		if onTimeout != nil {
			onTimeout()
		}
		return nil
	}
}

func (b *backend) Watch(ctx context.Context, s docstore.QuerySpec, deliver func([]docstore.Record) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.Filters = slices.Clone(s.Filters)
	s.Count = 200

	sub, ready, err := b.subscribeWatch(s.Collection)
	if err != nil {
		return err
	}
	defer b.unsubscribeWatch(sub)

	if err := waitWatchReady(ctx, ready, b.markWatchNeedsBroadcast); err != nil {
		return err
	}

	fallback := watchFallbackInterval
	if fallback <= 0 {
		fallback = defaultWatchFallbackInterval
	}
	timer := time.NewTimer(fallback)
	defer timer.Stop()

	for {
		select {
		case <-sub.ch:
		default:
		}

		rows, err := b.Query(ctx, s)
		if err != nil {
			return err
		}
		if len(rows) > 0 {
			if err := deliver(rows); err != nil {
				return err
			}
			if last := rows[len(rows)-1].Fields["seq"]; last != nil {
				for i := range s.Filters {
					if s.Filters[i].Field == "seq" && s.Filters[i].Op == ">" {
						s.Filters[i].Value = last
					}
				}
			}
			if len(rows) == s.Count {
				continue
			}
		}

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(fallback)

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-sub.ch:
		case <-timer.C:
		}
	}
}
