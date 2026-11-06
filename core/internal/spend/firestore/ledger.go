// Package firestore is the Firestore backend for spend.Ledger. It keeps the shared development budget in a Firestore
// database and applies the budget rules from package spend inside each transaction. Collection and field names are
// the saved layout and must not change.
package firestore

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
	"nori/internal/spend"
)

// Ledger implements spend.Ledger on Firestore.
type Ledger struct {
	store *firestore.Client
}

var _ spend.Ledger = (*Ledger)(nil)

// Open connects to the shared budget database. It refuses to run against an emulator: the shared budget is real
// money, and app data uses NORI_STATE_EMULATOR_HOST instead.
func Open(ctx context.Context, cfg spend.LedgerConfig) (*Ledger, error) {
	if cfg.Emulator {
		return nil, spend.Blocked("shared budget cannot use an emulator; use NORI_STATE_EMULATOR_HOST for app data")
	}
	store, err := firestore.NewClientWithDatabase(ctx, cfg.Project, cfg.Database)
	if err != nil {
		return nil, err
	}
	return &Ledger{store: store}, nil
}

// Opener adapts Open to spend.RegisterLedger.
func Opener(ctx context.Context, cfg spend.LedgerConfig) (spend.Ledger, error) { return Open(ctx, cfg) }

func (l *Ledger) Close() error { return l.store.Close() }

func (l *Ledger) scopeRef(id string) *firestore.DocumentRef {
	return l.store.Collection("development_spend_scopes").Doc(id)
}
func (l *Ledger) poolRef() *firestore.DocumentRef {
	return l.store.Collection("development_spend_pools").Doc("global")
}
func (l *Ledger) reservationRef(id string) *firestore.DocumentRef {
	return l.store.Collection("development_spend_reservations").Doc(id)
}

// decode reads a snapshot into out. A record that exists but does not decode wraps spend.ErrInvalidRecord.
func decode(snapshot *firestore.DocumentSnapshot, out any) error {
	if err := snapshot.DataTo(out); err != nil {
		return fmt.Errorf("%w: %v", spend.ErrInvalidRecord, err)
	}
	return nil
}

func (l *Ledger) Scope(ctx context.Context, id string) (spend.ScopeRecord, error) {
	var scope spend.ScopeRecord
	snapshot, err := l.scopeRef(id).Get(ctx)
	if err != nil {
		return scope, err
	}
	return scope, decode(snapshot, &scope)
}

func (l *Ledger) Pool(ctx context.Context) (spend.PoolRecord, error) {
	var pool spend.PoolRecord
	snapshot, err := l.poolRef().Get(ctx)
	if err != nil {
		return pool, err
	}
	return pool, decode(snapshot, &pool)
}

func (l *Ledger) readScopeAndPool(tx *firestore.Transaction, scopeID string) (spend.ScopeRecord, spend.PoolRecord, error) {
	var scope spend.ScopeRecord
	var pool spend.PoolRecord
	snapshot, err := tx.Get(l.scopeRef(scopeID))
	if err != nil {
		return scope, pool, err
	}
	if err = snapshot.DataTo(&scope); err != nil {
		return scope, pool, err
	}
	snapshot, err = tx.Get(l.poolRef())
	if err != nil {
		return scope, pool, err
	}
	return scope, pool, snapshot.DataTo(&pool)
}

func (l *Ledger) writePool(tx *firestore.Transaction, pool spend.PoolRecord) error {
	return tx.Update(l.poolRef(), []firestore.Update{
		{Path: "day", Value: pool.Day}, {Path: "month", Value: pool.Month},
		{Path: "day_known_nanos", Value: pool.DayKnown}, {Path: "month_known_nanos", Value: pool.MonthKnown},
		{Path: "pending_nanos", Value: pool.Pending}, {Path: "blocked", Value: pool.Blocked},
	})
}

func (l *Ledger) Reserve(ctx context.Context, req spend.ReserveRequest) error {
	return l.store.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		scope, pool, err := l.readScopeAndPool(tx, req.Scope)
		if err != nil {
			return err
		}
		scope, pool, reservation, err := spend.Admit(req, scope, pool, time.Now())
		if err != nil {
			return err
		}
		if err = tx.Create(l.reservationRef(req.ID), reservation); err != nil {
			return err
		}
		if err = tx.Update(l.scopeRef(req.Scope), []firestore.Update{{Path: "used_nanos", Value: scope.Used}}); err != nil {
			return err
		}
		return l.writePool(tx, pool)
	})
}

func (l *Ledger) Settle(ctx context.Context, req spend.SettleRequest) (bool, error) {
	overrun := false
	err := l.store.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		overrun = false
		ref := l.reservationRef(req.ID)
		snapshot, err := tx.Get(ref)
		if err != nil {
			return err
		}
		var reservation spend.ReservationRecord
		if err = snapshot.DataTo(&reservation); err != nil {
			return err
		}
		settled, err := spend.CheckSettlement(req, reservation)
		if err != nil || settled {
			return err
		}
		scope, pool, err := l.readScopeAndPool(tx, req.Scope)
		if err != nil {
			return err
		}
		scope, pool, overrun, err = spend.ApplySettlement(req, reservation, scope, pool, time.Now())
		if err != nil {
			return err
		}
		if err = tx.Update(ref, []firestore.Update{{Path: "settled", Value: true}}); err != nil {
			return err
		}
		if err = tx.Update(l.scopeRef(req.Scope), []firestore.Update{{Path: "used_nanos", Value: scope.Used}}); err != nil {
			return err
		}
		return l.writePool(tx, pool)
	})
	return overrun, err
}
