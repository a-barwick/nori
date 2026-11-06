// Package firestore is the Firestore backend for state.Database. It reads and writes documents inside Firestore
// transactions and calls the pure rules in package state to decide what to write. Its document paths, field names and
// values are the saved layout of existing production data and must not change.
package firestore

import (
	"context"
	"errors"
	"fmt"
	"net"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"nori/internal/state"
)

// Config selects the Firestore database one process uses.
type Config struct {
	Project       string
	Database      string
	TaskBudgetUSD float64
	// EmulatorAddress sends this client to a Firestore emulator on a loopback address, without credentials. It is
	// separate from FIRESTORE_EMULATOR_HOST, which the Firestore SDK reads for every client in the process.
	EmulatorAddress string
}

// Database implements state.Database on Firestore.
type Database struct {
	db            *firestore.Client
	taskBudgetUSD float64
}

var _ state.Database = (*Database)(nil)

// Open connects to the configured Firestore database.
func Open(ctx context.Context, cfg Config) (*Database, error) {
	var options []option.ClientOption
	if cfg.EmulatorAddress != "" {
		if !IsLoopback(cfg.EmulatorAddress) {
			return nil, fmt.Errorf("Firestore emulator address %q is not a loopback address", cfg.EmulatorAddress)
		}
		options = append(options, option.WithEndpoint(cfg.EmulatorAddress), option.WithoutAuthentication(),
			option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())))
	}
	db, err := firestore.NewClientWithDatabase(ctx, cfg.Project, cfg.Database, options...)
	if err != nil {
		return nil, err
	}
	return &Database{db: db, taskBudgetUSD: cfg.TaskBudgetUSD}, nil
}

// IsLoopback reports whether address is a host:port on this machine.
func IsLoopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	return err == nil && (host == "localhost" || (ip != nil && ip.IsLoopback()))
}

func (d *Database) Close() error { return d.db.Close() }

func (d *Database) root() *firestore.DocumentRef       { return d.db.Collection("owners").Doc(state.Owner) }
func (d *Database) runs() *firestore.CollectionRef     { return d.root().Collection("runs") }
func (d *Database) messages() *firestore.CollectionRef { return d.root().Collection("messages") }
func (d *Database) commands() *firestore.CollectionRef { return d.root().Collection("task_commands") }
func (d *Database) usage() *firestore.CollectionRef    { return d.root().Collection("model_usage") }
func (d *Database) control() *firestore.DocumentRef    { return d.db.Collection("system").Doc("control") }
func (d *Database) conversation() *firestore.DocumentRef {
	return d.root().Collection("conversation").Doc("control")
}
func (d *Database) conversationInputs() *firestore.CollectionRef {
	return d.root().Collection("conversation_inputs")
}
func (d *Database) conversationTurns() *firestore.CollectionRef {
	return d.root().Collection("conversation_turns")
}
func (d *Database) conversationAttempts() *firestore.CollectionRef {
	return d.root().Collection("conversation_attempts")
}

func notFound(err error) bool { return status.Code(err) == codes.NotFound }
func isDone(err error) bool   { return errors.Is(err, iterator.Done) }

// decodeRun maps a missing document to state.ErrNotFound.
func decodeRun(doc *firestore.DocumentSnapshot, err error) (state.Run, error) {
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

// documents decodes every remaining document of iter as T.
func documents[T any](iter *firestore.DocumentIterator) ([]T, error) {
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
func (d *Database) readConversation(tx *firestore.Transaction) (state.ConversationControl, error) {
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
	doc, err := d.control().Get(ctx)
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
	return d.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
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
			if err := tx.Update(parentRef, []firestore.Update{
				{Path: "subtask_spent_usd", Value: parent.SubtaskSpent}, {Path: "cost_unknown", Value: parent.CostUnknown},
			}); err != nil {
				return err
			}
		}
		return tx.Update(ref, []firestore.Update{
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

func (d *Database) Artifact(ctx context.Context, runID string, artifact state.Artifact) error {
	_, err := d.runs().Doc(runID).Update(ctx, []firestore.Update{{Path: "artifacts", Value: firestore.ArrayUnion(artifact)}})
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
			d.messages().OrderBy("created_at", firestore.Desc).Limit(state.SessionMessageLimit).Documents(ctx))
		if err != nil {
			return session, err
		}
		session.Messages = state.Chronological(messages)
	}
	recent, err := documents[state.Run](d.runs().OrderBy("started_at", firestore.Desc).Limit(state.SessionRunLimit).Documents(ctx))
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
		usage, err := documents[state.Usage](d.usage().OrderBy("created_at", firestore.Desc).Limit(state.SessionUsageLimit).Documents(ctx))
		if err != nil {
			return session, err
		}
		session.Usage = usage
	}
	return session, nil
}
