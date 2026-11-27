package push

import (
	"context"
	"errors"
	"strings"
	"time"
)

const (
	// DefaultDailyCap is the hard per-user 24-hour push limit enforced
	// independently of model behavior (P1 #12).
	DefaultDailyCap    = 10
	DefaultMaxAttempts = 5

	DefaultTitle = "Nori"
	DefaultBody  = "Ready to review"
)

// NotificationKind selects a fixed, privacy-safe generic lock-screen message.
type NotificationKind string

const (
	KindApproval NotificationKind = "approval"
	KindUrgent   NotificationKind = "urgent"
	KindBrief    NotificationKind = "brief"
	KindUpdate   NotificationKind = "update"
)

var allowedLockScreenBodies = map[string]struct{}{
	DefaultBody:                   {},
	"Approval ready to review":    {},
	"Something needs your review": {},
	"Your morning brief is ready": {},
	"Your update is ready":        {},
}

// CopyForKind returns the lock-screen-safe generic title and body for kind.
func CopyForKind(kind NotificationKind) (string, string) {
	switch kind {
	case KindApproval:
		return DefaultTitle, "Approval ready to review"
	case KindUrgent:
		return DefaultTitle, "Something needs your review"
	case KindBrief:
		return DefaultTitle, "Your morning brief is ready"
	case KindUpdate:
		return DefaultTitle, DefaultBody
	default:
		return DefaultTitle, DefaultBody
	}
}

// SanitizeLockScreenCopy guarantees lock-screen notifications only ever carry
// generic Nori copy and never leak sensitive email, calendar, or watch body text.
func SanitizeLockScreenCopy(title, body string) (string, string) {
	cleanTitle := strings.TrimSpace(title)
	if cleanTitle != DefaultTitle {
		cleanTitle = DefaultTitle
	}
	cleanBody := strings.TrimSpace(body)
	if _, ok := allowedLockScreenBodies[cleanBody]; !ok {
		cleanBody = DefaultBody
	}
	return cleanTitle, cleanBody
}

// EnqueueRequest describes a push notification to enqueue into the idempotent outbox.
type EnqueueRequest struct {
	DeliveryID    string
	UserID        string
	InteractionID string
	CollapseID    string
	Kind          NotificationKind
}

// ProcessStats summarizes a single ProcessPending pass.
type ProcessStats struct {
	Processed  int
	Sent       int
	Suppressed int
	Failed     int
	Retried    int
}

// OutboxWorker drains pending rows in nori_push_outbox with idempotency,
// lock-screen privacy enforcement, invalid token cleanup, and a hard per-user daily cap.
type OutboxWorker struct {
	Store       Store
	Sender      Sender
	DailyCap    int
	MaxAttempts int
	Now         func() time.Time
}

func (w *OutboxWorker) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

func (w *OutboxWorker) dailyCap() int {
	if w.DailyCap > 0 {
		return w.DailyCap
	}
	return DefaultDailyCap
}

func (w *OutboxWorker) maxAttempts() int {
	if w.MaxAttempts > 0 {
		return w.MaxAttempts
	}
	return DefaultMaxAttempts
}

// Enqueue inserts an idempotent outbox item keyed by req.DeliveryID with
// sanitized lock-screen copy. Repeating the same DeliveryID is a no-op and
// returns (existing, false, nil).
func (w *OutboxWorker) Enqueue(ctx context.Context, req EnqueueRequest) (OutboxItem, bool, error) {
	if w.Store == nil {
		return OutboxItem{}, false, errors.New("push: store is nil")
	}
	title, body := CopyForKind(req.Kind)
	collapseID := strings.TrimSpace(req.CollapseID)
	if collapseID == "" {
		collapseID = strings.TrimSpace(req.DeliveryID)
	}
	return w.Store.EnqueueOutbox(ctx, OutboxItem{
		DeliveryID:    strings.TrimSpace(req.DeliveryID),
		UserID:        strings.TrimSpace(req.UserID),
		InteractionID: strings.TrimSpace(req.InteractionID),
		CollapseID:    collapseID,
		Title:         title,
		Body:          body,
		Status:        StatusPending,
	})
}

// ProcessPending drains up to limit pending outbox items.
func (w *OutboxWorker) ProcessPending(ctx context.Context, limit int) (ProcessStats, error) {
	if w.Store == nil {
		return ProcessStats{}, errors.New("push: store is nil")
	}
	items, err := w.Store.ListPendingOutbox(ctx, limit)
	if err != nil {
		return ProcessStats{}, err
	}
	var stats ProcessStats
	for _, item := range items {
		stats.Processed++
		status, err := w.processItem(ctx, item)
		if err != nil {
			return stats, err
		}
		switch status {
		case StatusSent:
			stats.Sent++
		case StatusSuppressed:
			stats.Suppressed++
		case StatusFailed:
			stats.Failed++
		case StatusPending:
			stats.Retried++
		}
	}
	return stats, nil
}

// ProcessItem delivers a single outbox item if it is still pending.
func (w *OutboxWorker) ProcessItem(ctx context.Context, item OutboxItem) (OutboxStatus, error) {
	if w.Store == nil {
		return "", errors.New("push: store is nil")
	}
	return w.processItem(ctx, item)
}

func (w *OutboxWorker) processItem(ctx context.Context, item OutboxItem) (OutboxStatus, error) {
	if item.Status != StatusPending {
		return item.Status, nil
	}
	if w.Sender == nil {
		return StatusSuppressed, w.Store.MarkOutboxSuppressed(ctx, item.DeliveryID, "push_sender_disabled")
	}

	now := w.now()
	sentLast24h, err := w.Store.CountSentSince(ctx, item.UserID, now.Add(-24*time.Hour))
	if err != nil {
		return "", err
	}
	if sentLast24h >= w.dailyCap() {
		if err := w.Store.MarkOutboxSuppressed(ctx, item.DeliveryID, "daily_cap_reached"); err != nil {
			return "", err
		}
		return StatusSuppressed, nil
	}

	devices, err := w.Store.ListDevices(ctx, item.UserID)
	if err != nil {
		return "", err
	}
	if len(devices) == 0 {
		if err := w.Store.MarkOutboxSuppressed(ctx, item.DeliveryID, "no_registered_devices"); err != nil {
			return "", err
		}
		return StatusSuppressed, nil
	}

	title, body := SanitizeLockScreenCopy(item.Title, item.Body)
	notif := Notification{
		DeliveryID:    item.DeliveryID,
		CollapseID:    item.CollapseID,
		InteractionID: item.InteractionID,
		Title:         title,
		Body:          body,
	}

	var sentCount int
	var invalidCount int
	var lastErr error
	allPermanent := true

	for _, dev := range devices {
		err := w.Sender.Send(ctx, dev, notif)
		if err == nil {
			sentCount++
			continue
		}
		lastErr = err
		if IsInvalidTokenError(err) {
			invalidCount++
			_ = w.Store.DeleteDeviceByToken(ctx, dev.Token)
			continue
		}
		if !IsPermanentError(err) {
			allPermanent = false
		}
	}

	if sentCount > 0 {
		if err := w.Store.MarkOutboxSent(ctx, item.DeliveryID, now); err != nil {
			return "", err
		}
		return StatusSent, nil
	}

	if invalidCount == len(devices) {
		if err := w.Store.MarkOutboxSuppressed(ctx, item.DeliveryID, "all_tokens_invalid"); err != nil {
			return "", err
		}
		return StatusSuppressed, nil
	}

	permanent := allPermanent || (item.Attempts+1 >= w.maxAttempts())
	errMsg := ""
	if lastErr != nil {
		errMsg = lastErr.Error()
	}
	if err := w.Store.MarkOutboxFailed(ctx, item.DeliveryID, errMsg, permanent); err != nil {
		return "", err
	}
	if permanent {
		return StatusFailed, nil
	}
	return StatusPending, nil
}
