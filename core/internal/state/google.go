package state

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/storage"
	"github.com/google/uuid"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Google struct {
	db      *firestore.Client
	objects *storage.Client
	bucket  string
}

func NewGoogle(ctx context.Context, project, database, bucket string) (*Google, error) {
	db, e := firestore.NewClientWithDatabase(ctx, project, database)
	if e != nil {
		return nil, e
	}
	objects, e := storage.NewClient(ctx)
	if e != nil {
		db.Close()
		return nil, e
	}
	return &Google{db: db, objects: objects, bucket: bucket}, nil
}
func (s *Google) Close() error                       { return errors.Join(s.db.Close(), s.objects.Close()) }
func (s *Google) root() *firestore.DocumentRef       { return s.db.Collection("owners").Doc(Owner) }
func (s *Google) runs() *firestore.CollectionRef     { return s.root().Collection("runs") }
func (s *Google) messages() *firestore.CollectionRef { return s.root().Collection("messages") }
func (s *Google) Control(ctx context.Context) (Control, error) {
	d, e := s.db.Collection("system").Doc("control").Get(ctx)
	if e != nil {
		return Control{}, e
	}
	var c Control
	e = d.DataTo(&c)
	return c, e
}
func decodeRun(d *firestore.DocumentSnapshot, e error) (Run, error) {
	if status.Code(e) == codes.NotFound {
		return Run{}, ErrNotFound
	}
	if e != nil {
		return Run{}, e
	}
	var r Run
	e = d.DataTo(&r)
	return r, e
}
func (s *Google) GetRun(ctx context.Context, id string) (Run, error) {
	return decodeRun(s.runs().Doc(id).Get(ctx))
}
func (s *Google) Begin(ctx context.Context, id, text string) (Run, error) {
	var run Run
	err := s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		control, e := tx.Get(s.db.Collection("system").Doc("control"))
		if e != nil {
			return e
		}
		var c Control
		if e = control.DataTo(&c); e != nil {
			return e
		}
		expiry, e := c.Deadline()
		if e != nil {
			return e
		}
		_, e = tx.Get(s.runs().Doc(id))
		if e == nil {
			return ErrDuplicate
		}
		if status.Code(e) != codes.NotFound {
			return e
		}
		root, e := tx.Get(s.root())
		if e != nil && status.Code(e) != codes.NotFound {
			return e
		}
		active := ""
		if e == nil {
			active, _ = root.Data()["active_run"].(string)
		}
		var previous *Run
		if active != "" {
			d, e := tx.Get(s.runs().Doc(active))
			if e != nil {
				return e
			}
			r, e := decodeRun(d, nil)
			if e != nil {
				return e
			}
			if r.Status == "running" {
				if r.Temporal || r.Deadline.Add(CleanupGrace).After(time.Now()) {
					return ErrBusy
				}
				previous = &r
			}
		}
		now := time.Now().UTC()
		deadline := now.Add(2 * time.Minute)
		if expiry.Before(deadline) {
			deadline = expiry
		}
		run = Run{ID: id, Status: "running", StartedAt: now, Deadline: deadline, Events: []Event{}, Artifacts: []Artifact{}}
		if previous != nil {
			previous.Status = "failed"
			previous.Error = "Interrupted: execution deadline expired; no automatic recovery."
			previous.FinishedAt = &now
			if e = tx.Set(s.runs().Doc(previous.ID), previous); e != nil {
				return e
			}
		}
		if e = tx.Set(s.root(), map[string]any{"active_run": id}, firestore.MergeAll); e != nil {
			return e
		}
		if e = tx.Create(s.runs().Doc(id), run); e != nil {
			return e
		}
		return tx.Create(s.messages().Doc(id+"-user"), Message{ID: id + "-user", RunID: id, Role: "user", Text: text, CreatedAt: now})
	})
	return run, err
}
func (s *Google) RequestCancel(ctx context.Context, id string) (Run, error) {
	var run Run
	e := s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		d, e := tx.Get(s.runs().Doc(id))
		run, e = decodeRun(d, e)
		if e != nil {
			return e
		}
		if run.Status != "running" {
			return nil
		}
		run.CancelRequested = true
		return tx.Update(s.runs().Doc(id), []firestore.Update{{Path: "cancel_requested", Value: true}})
	})
	return run, e
}
func (s *Google) Event(ctx context.Context, id string, event Event) error {
	event.CreatedAt = time.Now().UTC()
	_, e := s.runs().Doc(id).Update(ctx, []firestore.Update{{Path: "events", Value: firestore.ArrayUnion(event)}})
	return e
}
func (s *Google) Artifact(ctx context.Context, id string, a Artifact) error {
	_, e := s.runs().Doc(id).Update(ctx, []firestore.Update{{Path: "artifacts", Value: firestore.ArrayUnion(a)}})
	return e
}
func (s *Google) Finish(ctx context.Context, id, runStatus, detail, text string) (Run, error) {
	var r Run
	e := s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		d, e := tx.Get(s.runs().Doc(id))
		r, e = decodeRun(d, e)
		if e != nil {
			return e
		}
		if r.Status != "running" {
			return nil
		}
		now := time.Now().UTC()
		if runStatus == "completed" && r.CancelRequested {
			runStatus = "cancelled"
			detail = "Cancelled by Austin."
			text = ""
		}
		r.Status = runStatus
		r.Error = detail
		r.FinishedAt = &now
		if e = tx.Set(s.runs().Doc(id), r); e != nil {
			return e
		}
		if text != "" {
			return tx.Create(s.messages().Doc(id+"-assistant"), Message{ID: id + "-assistant", RunID: id, Role: "assistant", Text: text, CreatedAt: now})
		}
		return nil
	})
	return r, e
}
func (s *Google) Session(ctx context.Context) (Session, error) {
	c, e := s.Control(ctx)
	if e != nil {
		return Session{}, e
	}
	session := Session{Messages: []Message{}, Runs: []Run{}, Paused: c.Paused, ExpiresAt: c.ExpiresAt}
	iter := s.messages().OrderBy("created_at", firestore.Desc).Limit(100).Documents(ctx)
	defer iter.Stop()
	for {
		d, e := iter.Next()
		if e == iterator.Done {
			break
		}
		if e != nil {
			return session, e
		}
		var m Message
		if e = d.DataTo(&m); e != nil {
			return session, e
		}
		session.Messages = append(session.Messages, m)
	}
	for i, j := 0, len(session.Messages)-1; i < j; i, j = i+1, j-1 {
		session.Messages[i], session.Messages[j] = session.Messages[j], session.Messages[i]
	}
	runs := s.runs().OrderBy("started_at", firestore.Desc).Limit(30).Documents(ctx)
	defer runs.Stop()
	for {
		d, e := runs.Next()
		if e == iterator.Done {
			break
		}
		if e != nil {
			return session, e
		}
		r, e := decodeRun(d, nil)
		if e != nil {
			return session, e
		}
		if r.Status == "running" && !r.Temporal && !r.Deadline.Add(CleanupGrace).After(time.Now()) {
			r, e = s.Finish(ctx, r.ID, "failed", "Interrupted: execution deadline expired; no automatic recovery.", "")
			if e != nil {
				return session, e
			}
		}
		session.Runs = append(session.Runs, r)
	}
	return session, nil
}
func objectPath(run, id string) string { return "artifacts/" + run + "/" + id + ".png" }
func (s *Google) PutPNG(ctx context.Context, run, id string, data []byte) error {
	if _, e := uuid.Parse(run); e != nil {
		return e
	}
	if _, e := uuid.Parse(id); e != nil {
		return e
	}
	if len(data) > 2<<20 {
		return fmt.Errorf("screenshot exceeds 2 MiB")
	}
	w := s.objects.Bucket(s.bucket).Object(objectPath(run, id)).If(storage.Conditions{DoesNotExist: true}).NewWriter(ctx)
	w.ContentType = "image/png"
	w.CacheControl = "private, no-store"
	if _, e := io.Copy(w, bytes.NewReader(data)); e != nil {
		_ = w.Close()
		return e
	}
	return w.Close()
}
func (s *Google) GetPNG(ctx context.Context, run, id string) ([]byte, error) {
	r, e := s.GetRun(ctx, run)
	if e != nil {
		return nil, e
	}
	found := false
	for _, a := range r.Artifacts {
		if a.ID == id {
			found = true
		}
	}
	if !found {
		return nil, ErrNotFound
	}
	reader, e := s.objects.Bucket(s.bucket).Object(objectPath(run, id)).NewReader(ctx)
	if e != nil {
		return nil, e
	}
	defer reader.Close()
	data, e := io.ReadAll(io.LimitReader(reader, (2<<20)+1))
	if len(data) > 2<<20 {
		return nil, fmt.Errorf("artifact exceeds limit")
	}
	return data, e
}
