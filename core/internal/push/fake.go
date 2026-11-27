package push

import (
	"context"
	"sync"
	"time"
)

// SentPush records a single push notification sent via FakeSender.
type SentPush struct {
	Device       Device
	Notification Notification
	SentAt       time.Time
}

// FakeSender is a deterministic, in-memory Sender for unit tests and DST.
type FakeSender struct {
	mu          sync.Mutex
	Now         func() time.Time
	SendFn      func(ctx context.Context, device Device, n Notification) error
	TokenErrors map[string]error
	NextErr     error
	sent        []SentPush
}

// NewFakeSender returns a ready-to-use FakeSender.
func NewFakeSender() *FakeSender {
	return &FakeSender{
		TokenErrors: make(map[string]error),
	}
}

func (f *FakeSender) Send(ctx context.Context, device Device, n Notification) error {
	f.mu.Lock()
	sendFn := f.SendFn
	var injectedErr error
	if err, ok := f.TokenErrors[device.Token]; ok {
		injectedErr = err
	} else if f.NextErr != nil {
		injectedErr = f.NextErr
		f.NextErr = nil
	}
	nowFn := f.Now
	f.mu.Unlock()

	if sendFn != nil {
		if err := sendFn(ctx, device, n); err != nil {
			return err
		}
	}
	if injectedErr != nil {
		return injectedErr
	}

	now := time.Now().UTC()
	if nowFn != nil {
		now = nowFn().UTC()
	}

	f.mu.Lock()
	f.sent = append(f.sent, SentPush{
		Device:       device,
		Notification: n,
		SentAt:       now,
	})
	f.mu.Unlock()
	return nil
}

// Sent returns a snapshot copy of all successfully delivered pushes.
func (f *FakeSender) Sent() []SentPush {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]SentPush, len(f.sent))
	copy(out, f.sent)
	return out
}

// Count returns the number of successfully delivered pushes.
func (f *FakeSender) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// Reset clears recorded pushes and transient errors.
func (f *FakeSender) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = nil
	f.NextErr = nil
}
