package wake

import (
	"context"
	"sync"
	"time"

	"nori/internal/state"
)

type Repairer struct {
	Waker   Waker
	Pending func(context.Context) (string, error)
	Now     func() time.Time

	mu      sync.Mutex
	last    time.Time
	version string
}

func (r *Repairer) now() time.Time {
	if r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

func (r *Repairer) Repair(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	version, err := r.Pending(ctx)
	if err != nil || version == "" || state.IsTaskOnlyWorkVersion(version) {
		return err
	}
	now := r.now()
	r.mu.Lock()
	throttled := version == r.version && now.Sub(r.last) < time.Minute
	r.mu.Unlock()
	if throttled {
		return nil
	}
	if err := r.Waker.WakeConversation(ctx); err != nil {
		return err
	}
	done := r.now()
	r.mu.Lock()
	if r.version != version || done.After(r.last) {
		r.last = done
		r.version = version
	}
	r.mu.Unlock()
	return nil
}
