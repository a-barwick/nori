package wake

import (
	"context"
	"sync"
	"time"
)

// Repairer reconstructs disposable API notifications from committed work. Every
// admission caller still confirms its own wake after acceptance; readers and worker
// polling share this fallback, including work unrelated to the displayed chat.
type Repairer struct {
	Waker   Waker
	Pending func(context.Context) (string, error)

	mu      sync.Mutex
	last    time.Time
	version string
}

func (r *Repairer) Repair(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	version, err := r.Pending(ctx)
	if err != nil || version == "" {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if version == r.version && time.Since(r.last) < time.Minute {
		return nil
	}
	if err := r.Waker.WakeConversation(ctx); err != nil {
		// A failed enqueue is not a confirmed wake. In particular, the next
		// worker slice must retry instead of acknowledging the only notification.
		return err
	}
	r.last = time.Now()
	r.version = version
	return nil
}
