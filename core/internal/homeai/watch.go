package homeai

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

func notify(ctx context.Context, tx pgx.Tx, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_notify('nori_home_ai_events',$1)`, key)
	return err
}

func (s *Service) subscribe(key string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	s.mu.Lock()
	if s.waiters[key] == nil {
		s.waiters[key] = map[chan struct{}]struct{}{}
	}
	s.waiters[key][ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.waiters[key], ch)
		if len(s.waiters[key]) == 0 {
			delete(s.waiters, key)
		}
		s.mu.Unlock()
	}
}

func (s *Service) wake(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.waiters[key] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (s *Service) wakeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, group := range s.waiters {
		for ch := range group {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
	}
}

func (s *Service) watch(ctx context.Context) {
	defer close(s.done)
	for ctx.Err() == nil {
		conn, err := pgx.Connect(ctx, s.listenURL)
		if err == nil {
			_, err = conn.Exec(ctx, `LISTEN nori_home_ai_events`)
			if err == nil {
				s.wakeAll()
				for ctx.Err() == nil {
					waitCtx, cancel := context.WithTimeout(ctx, time.Minute)
					n, waitErr := conn.WaitForNotification(waitCtx)
					cancel()
					if n != nil {
						s.wake(n.Payload)
					}
					if waitErr != nil && ctx.Err() == nil && !errors.Is(waitErr, context.DeadlineExceeded) {
						break
					}
				}
			}
			closeCtx, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = conn.Close(closeCtx)
			closeCancel()
		}
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Service) cleanupLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			_ = s.cleanup(cleanupCtx)
			cancel()
		}
	}
}
