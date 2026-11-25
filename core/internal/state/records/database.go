// Package records implements Nori persistence rules over a transactional document backend.
package records

import (
	"context"
	"errors"
	"time"

	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/users"
)

type Database struct {
	db            *docstore.Client
	taskBudgetUSD float64
	user          users.User
	Now           func() time.Time
}

func (d *Database) now() time.Time {
	if d.Now == nil {
		return time.Now().UTC()
	}
	return d.Now().UTC()
}

var _ state.Database = (*Database)(nil)

func New(backend docstore.Backend, user users.User, taskBudgetUSD float64) *Database {
	return &Database{db: docstore.New(backend), user: user, taskBudgetUSD: taskBudgetUSD}
}

// NewShared creates a user-scoped Database over a shared backend whose lifetime is owned by the caller.
func NewShared(backend docstore.Backend, user users.User, taskBudgetUSD float64) *Database {
	var wrapped docstore.Backend = unclosedBackend{backend}
	if sr, ok := backend.(docstore.SequenceReader); ok {
		wrapped = unclosedSequencedBackend{unclosedBackend{backend}, sr}
	}
	return &Database{db: docstore.New(wrapped), user: user, taskBudgetUSD: taskBudgetUSD}
}

type unclosedBackend struct{ docstore.Backend }

func (unclosedBackend) Close() error               { return nil }
func (u unclosedBackend) Unwrap() docstore.Backend { return u.Backend }

type unclosedSequencedBackend struct {
	unclosedBackend
	docstore.SequenceReader
}

func (d *Database) Close() error { return d.db.Close() }

func (d *Database) root() *docstore.DocumentRef {
	if !users.ValidID(d.user.ID) {
		panic("document store requires a user scope")
	}
	return d.db.Collection("owners").Doc(d.user.DataOwner())
}
func (d *Database) runs() *docstore.CollectionRef     { return d.root().Collection("runs") }
func (d *Database) messages() *docstore.CollectionRef { return d.root().Collection("messages") }
func (d *Database) commands() *docstore.CollectionRef { return d.root().Collection("task_commands") }
func (d *Database) usage() *docstore.CollectionRef    { return d.root().Collection("model_usage") }
func controlDoc(db *docstore.Client) *docstore.DocumentRef {
	return db.Collection("system").Doc("control")
}
func (d *Database) conversation() *docstore.DocumentRef {
	return d.root().Collection("conversation").Doc("control")
}
func (d *Database) conversationInputs() *docstore.CollectionRef {
	return d.root().Collection("conversation_inputs")
}
func (d *Database) conversationTurns() *docstore.CollectionRef {
	return d.root().Collection("conversation_turns")
}
func (d *Database) conversationAttempts() *docstore.CollectionRef {
	return d.root().Collection("conversation_attempts")
}

func notFound(err error) bool { return errors.Is(err, docstore.ErrNotFound) }
func isDone(err error) bool   { return errors.Is(err, docstore.Done) }

func decodeRun(doc *docstore.DocumentSnapshot, err error) (state.Run, error) {
	if notFound(err) {
		return state.Run{}, state.ErrNotFound
	}
	if err != nil {
		return state.Run{}, err
	}
	var run state.Run
	err = doc.DataTo(&run)
	return run, err
}

func documents[T any](iter *docstore.DocumentIterator) ([]T, error) {
	defer iter.Stop()
	records := []T{}
	for {
		doc, err := iter.Next()
		if isDone(err) {
			return records, nil
		}
		if err != nil {
			return nil, err
		}
		var record T
		if err = doc.DataTo(&record); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
}

// readConversation reads the conversation control record inside a transaction. A conversation that has none yet reads
// as a new one.
func (d *Database) readConversation(tx *docstore.Transaction) (state.ConversationControl, error) {
	doc, err := tx.Get(d.conversation())
	if notFound(err) {
		return state.ConversationControl{}, nil
	}
	var c state.ConversationControl
	if err != nil {
		return c, err
	}
	err = doc.DataTo(&c)
	return c, err
}

func (d *Database) Control(ctx context.Context) (state.Control, error) {
	return readControl(ctx, controlDoc(d.db))
}

func readControl(ctx context.Context, ref *docstore.DocumentRef) (state.Control, error) {
	doc, err := ref.Get(ctx)
	if err != nil {
		return state.Control{}, err
	}
	var c state.Control
	err = doc.DataTo(&c)
	return c, err
}

func (d *Database) GetRun(ctx context.Context, id string) (state.Run, error) {
	return decodeRun(d.runs().Doc(id).Get(ctx))
}

func (d *Database) Spend(ctx context.Context, update state.SpendUpdate) error {
	return d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		ref := d.runs().Doc(update.RunID)
		run, err := decodeRun(tx.Get(ref))
		if err != nil {
			return err
		}
		spent, budget, costUnknown := state.MergeSpend(run, update)
		if run.ParentID != "" {
			parentRef := d.runs().Doc(run.ParentID)
			parent, err := decodeRun(tx.Get(parentRef))
			if err != nil {
				return err
			}
			parent = state.RollUpSpend(parent, run, update)
			if err := tx.Update(parentRef, []docstore.Update{
				{Path: "subtask_spent_usd", Value: parent.SubtaskSpent}, {Path: "cost_unknown", Value: parent.CostUnknown},
			}); err != nil {
				return err
			}
		}
		return tx.Update(ref, []docstore.Update{
			{Path: "spent_usd", Value: spent}, {Path: "budget_usd", Value: budget}, {Path: "cost_unknown", Value: costUnknown},
		})
	})
}

// RecordUsage is idempotent for one observed provider request. The caller keeps the same ID and timestamp when
// retrying a failed projection.
func (d *Database) RecordUsage(ctx context.Context, usage state.Usage) error {
	_, err := d.usage().Doc(usage.ID).Set(ctx, usage)
	return err
}

// ReserveUsage atomically checks rolling per-user hourly call and daily spend+exposure caps
// and writes the pending usage row inside a single transaction, eliminating TOCTOU races across
// concurrent requests and replicas. Estimated exposure is kept separate from recorded CostUSD.
func (d *Database) ReserveUsage(
	ctx context.Context,
	u state.Usage,
	hourCutoff, dayCutoff time.Time,
	maxCallsPerHour int,
	maxUSDPerDay float64,
	unknownEstimateUSD float64,
) (bool, error) {
	since := dayCutoff
	if hourCutoff.Before(since) {
		since = hourCutoff
	}
	allowed := false
	err := d.db.RunTransaction(ctx, func(ctx context.Context, tx *docstore.Transaction) error {
		allowed = false
		// Serialize concurrent quota admissions for this user via a dedicated lock document.
		quotaRef := d.root().Collection("meta").Doc("usage_quota")
		var quotaDoc struct {
			Seq int64 `firestore:"seq"`
		}
		if snap, err := tx.Get(quotaRef); err == nil && snap.Exists() {
			_ = snap.DataTo(&quotaDoc)
		} else if err != nil && !errors.Is(err, docstore.ErrNotFound) {
			return err
		}
		// Idempotent re-check if this exact usage ID was already reserved.
		if existingSnap, err := tx.Get(d.usage().Doc(u.ID)); err == nil && existingSnap.Exists() {
			allowed = true
			return nil
		} else if err != nil && !errors.Is(err, docstore.ErrNotFound) {
			return err
		}
		rows, err := documents[state.Usage](tx.Documents(d.usage().Where("created_at", ">=", since)))
		if err != nil {
			return err
		}
		var hourCalls int
		var dayActualUSD, dayExposureUSD float64
		for _, r := range rows {
			if r.RequestStatus == "not_sent" || r.ID == u.ID {
				continue
			}
			if !r.CreatedAt.Before(hourCutoff) {
				hourCalls++
			}
			if !r.CreatedAt.Before(dayCutoff) {
				if r.CostUSD != nil && *r.CostUSD > 0 {
					dayActualUSD += *r.CostUSD
				} else if r.CostStatus != "provider" && r.CostStatus != "calculated" {
					if r.EstimatedExposureUSD > 0 {
						dayExposureUSD += r.EstimatedExposureUSD
					} else {
						dayExposureUSD += unknownEstimateUSD
					}
				}
			}
		}
		if maxCallsPerHour > 0 && hourCalls+1 > maxCallsPerHour {
			return nil
		}
		incomingExposure := u.EstimatedExposureUSD
		if incomingExposure <= 0 {
			incomingExposure = unknownEstimateUSD
		}
		if maxUSDPerDay > 0 && dayActualUSD+dayExposureUSD+incomingExposure > maxUSDPerDay {
			return nil
		}
		u.EstimatedExposureUSD = incomingExposure
		quotaDoc.Seq++
		if err := tx.Set(quotaRef, map[string]any{"seq": quotaDoc.Seq, "updated_at": u.CreatedAt}); err != nil {
			return err
		}
		if err := tx.Set(d.usage().Doc(u.ID), u); err != nil {
			return err
		}
		allowed = true
		return nil
	})
	return allowed, err
}

// UsageWindowSummary aggregates durable usage rows for this user in a single scan
// since dayCutoff so multi-replica Cloud Run instances enforce shared per-user
// hourly call and daily spend admission limits.
func (d *Database) UsageWindowSummary(ctx context.Context, hourCutoff, dayCutoff time.Time) (hourCalls int, daySpendUSD float64, dayUnknownCalls int, err error) {
	since := dayCutoff
	if hourCutoff.Before(since) {
		since = hourCutoff
	}
	rows, err := documents[state.Usage](d.usage().Where("created_at", ">=", since).Documents(ctx))
	if err != nil {
		return 0, 0, 0, err
	}
	for _, r := range rows {
		if r.RequestStatus == "not_sent" {
			continue
		}
		if !r.CreatedAt.Before(hourCutoff) {
			hourCalls++
		}
		if !r.CreatedAt.Before(dayCutoff) {
			if r.CostUSD != nil && *r.CostUSD > 0 {
				daySpendUSD += *r.CostUSD
			} else if r.CostStatus != "provider" && r.CostStatus != "calculated" {
				dayUnknownCalls++
			}
		}
	}
	return hourCalls, daySpendUSD, dayUnknownCalls, nil
}

func (d *Database) Artifact(ctx context.Context, runID string, artifact state.Artifact) error {
	_, err := d.runs().Doc(runID).Update(ctx, []docstore.Update{{Path: "artifacts", Value: docstore.ArrayUnion(artifact)}})
	return err
}

func (d *Database) Session(ctx context.Context) (state.Session, error) {
	return d.session(ctx, sessionWithMessages)
}

// ConversationProjection returns runs and usage without messages. Full provider turns are selected separately;
// messages stay cursor-accessible.
func (d *Database) ConversationProjection(ctx context.Context) (state.Session, error) {
	return d.session(ctx, conversationProjection)
}

type sessionMode int

const (
	sessionWithMessages sessionMode = iota
	conversationProjection
)

func (d *Database) session(ctx context.Context, mode sessionMode) (state.Session, error) {
	control, err := d.Control(ctx)
	if err != nil {
		return state.Session{}, err
	}
	session := state.Session{Messages: []state.Message{}, Runs: []state.Run{}, Paused: control.Paused}
	if mode == sessionWithMessages {
		messages, err := documents[state.Message](
			d.messages().OrderBy("created_at", docstore.Desc).Limit(state.SessionMessageLimit).Documents(ctx))
		if err != nil {
			return session, err
		}
		session.Messages = state.Chronological(messages)
	}
	recent, err := documents[state.Run](d.runs().OrderBy("started_at", docstore.Desc).Limit(state.SessionRunLimit).Documents(ctx))
	if err != nil {
		return session, err
	}
	session.Runs = recent
	// Waiting tasks remain visible even after thirty newer tasks have started.
	open, err := d.OpenTasks(ctx)
	if err != nil {
		return session, err
	}
	session.Runs = state.MergeOpenRuns(recent, open)
	if mode == conversationProjection {
		usage, err := documents[state.Usage](d.usage().OrderBy("created_at", docstore.Desc).Limit(state.SessionUsageLimit).Documents(ctx))
		if err != nil {
			return session, err
		}
		session.Usage = usage
	}
	return session, nil
}
