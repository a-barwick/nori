package platform

import (
	"context"
	"time"

	"nori/internal/secret"
	"nori/internal/secret/awssecrets"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/users"
	"nori/internal/wake"
)

// Waker returns a Postgres-backed wake.Waker when leases is non-nil, writing durable
// user wakes (with post-commit NOTIFY) to nori_user_wakes. When leases is nil, it
// returns wake.Disabled{}.
func Waker(leases docstore.LeaseStore) wake.Waker {
	if leases == nil {
		return wake.Disabled{}
	}
	return leaseWaker{leases: leases}
}

type leaseWaker struct {
	leases docstore.LeaseStore
}

func (w leaseWaker) WakeConversation(ctx context.Context) error {
	userID := users.ID(ctx)
	if userID == "" {
		return nil
	}
	return w.leases.WakeUser(ctx, userID, docstore.LaneConversation, time.Time{})
}

func (w leaseWaker) WakeConversationAt(ctx context.Context, at time.Time) error {
	userID := users.ID(ctx)
	if userID == "" {
		return nil
	}
	return w.leases.WakeUser(ctx, userID, docstore.LaneScheduler, at)
}

func (leaseWaker) WakeWorker(context.Context, string, ...string) error { return nil }
func (leaseWaker) WorkersNeedWake() bool                               { return false }

func Secrets(s settings.Settings) secret.Lookup {
	if s.Cloud == "aws" {
		return awssecrets.Lookup{Region: s.AWS.Region}
	}
	return nil
}

func WorkWakes(store state.Database, waker wake.Waker, tasks bool) *wake.Repairer {
	return &wake.Repairer{Waker: waker, Pending: func(ctx context.Context) (string, error) {
		return store.WorkVersion(ctx, tasks)
	}}
}
