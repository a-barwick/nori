package docstore

import (
	"context"
	"time"
)

// UsageReserveRequest carries everything needed by a backend (Postgres or memstore)
// to atomically serialize a user's entitlement/credit/rate/global-budget check and
// insert the pending state.Usage row in a single database transaction.
type UsageReserveRequest struct {
	UserID                   string
	DataOwner                string
	Collection               string
	UsageID                  string
	UsageFields              map[string]any
	Billing                  string
	Category                 string
	CostStatus               string
	EstimatedExposureUSD     float64
	UnknownEstimateUSD       float64
	Now                      time.Time
	HourCutoff               time.Time
	DayCutoff                time.Time
	FallbackMaxCallsPerHour  int
	FallbackMaxUSDPerDay     float64
	HostedAllMaxMicrosPerDay int64
	HostedExaMaxMicrosPerDay int64
	EnforceEntitlement       bool
}

type UsageReserveResult struct {
	Allowed             bool
	UsedEntitlement     bool
	ReservedExposureUSD float64
	EffectiveBilling    string
	EffectiveCategory   string
}

type UsageReconcileRequest struct {
	Collection           string
	UsageID              string
	UsageFields          map[string]any
	Billing              string
	Category             string
	CostStatus           string
	EstimatedExposureUSD float64
	CostUSD              *float64
	RequestStatus        string
	CreatedAt            time.Time
}

// AtomicBillingReserver is implemented by backends (Postgres, memstore) that support
// single-transaction entitlement FOR UPDATE reservation and global budget checks.
type AtomicBillingReserver interface {
	ReserveUsageWithBilling(ctx context.Context, req UsageReserveRequest) (UsageReserveResult, error)
}

// UsageFinalizationReconciler is implemented by backends that update a usage document
// and reconcile any global daily hosted budget delta (actual - reserved) atomically.
type UsageFinalizationReconciler interface {
	RecordAndReconcileUsage(ctx context.Context, req UsageReconcileRequest) error
}

// TrialProvisioner is implemented by transaction backends to atomically create a new user's
// trial entitlement, one-time $5.00 credit grant, and daily signup cap check in the same tx
// as inserting the user record.
type TrialProvisioner interface {
	ProvisionTrial(ctx context.Context, userID string, now time.Time, maxTrialsPerDay int) error
}

// ReserveUsageWithBilling unwraps backend wrappers to invoke AtomicBillingReserver if supported.
func (c *Client) ReserveUsageWithBilling(ctx context.Context, req UsageReserveRequest) (UsageReserveResult, bool, error) {
	for backend := c.Backend; backend != nil; {
		if r, ok := backend.(AtomicBillingReserver); ok {
			res, err := r.ReserveUsageWithBilling(ctx, req)
			return res, true, err
		}
		wrapper, ok := backend.(BackendUnwrapper)
		if !ok {
			break
		}
		backend = wrapper.Unwrap()
	}
	return UsageReserveResult{}, false, nil
}

// RecordAndReconcileUsage unwraps backend wrappers to invoke UsageFinalizationReconciler if supported.
func (c *Client) RecordAndReconcileUsage(ctx context.Context, req UsageReconcileRequest) (bool, error) {
	for backend := c.Backend; backend != nil; {
		if r, ok := backend.(UsageFinalizationReconciler); ok {
			return true, r.RecordAndReconcileUsage(ctx, req)
		}
		wrapper, ok := backend.(BackendUnwrapper)
		if !ok {
			break
		}
		backend = wrapper.Unwrap()
	}
	return false, nil
}

// ProvisionTrial invokes TrialProvisioner on the transaction backend when available.
func (t *Transaction) ProvisionTrial(userID string, now time.Time, maxTrialsPerDay int) error {
	p, ok := t.backend.(TrialProvisioner)
	if !ok {
		return nil
	}
	return p.ProvisionTrial(t.ctx, userID, now, maxTrialsPerDay)
}
