package state

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/storage"
	"github.com/google/uuid"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

type Google struct {
	db             *firestore.Client
	objects        *storage.Client
	bucket         string
	localArtifacts *os.Root
}

func NewGoogle(ctx context.Context, project, database, bucket string) (*Google, error) {
	localArtifacts, e := openLocalArtifacts()
	if e != nil {
		return nil, e
	}
	// An app-only emulator endpoint keeps development spend enforcement on its
	// real shared database. FIRESTORE_EMULATOR_HOST still supports unpaid tests.
	var options []option.ClientOption
	if address := os.Getenv("NORI_STATE_EMULATOR_HOST"); address != "" {
		if !loopbackEmulator(address) || os.Getenv("FIRESTORE_EMULATOR_HOST") != "" {
			if localArtifacts != nil {
				_ = localArtifacts.Close()
			}
			return nil, fmt.Errorf("NORI_STATE_EMULATOR_HOST requires loopback and no FIRESTORE_EMULATOR_HOST")
		}
		options = append(options, option.WithEndpoint(address), option.WithoutAuthentication(), option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())))
	}
	db, e := firestore.NewClientWithDatabase(ctx, project, database, options...)
	if e != nil {
		if localArtifacts != nil {
			_ = localArtifacts.Close()
		}
		return nil, e
	}
	store := &Google{db: db, bucket: bucket, localArtifacts: localArtifacts}
	if localArtifacts != nil {
		return store, nil
	}
	store.objects, e = storage.NewClient(ctx)
	if e != nil {
		_ = db.Close()
		return nil, e
	}
	return store, nil
}

func openLocalArtifacts() (*os.Root, error) {
	directory := os.Getenv("NORI_LOCAL_ARTIFACT_DIR")
	if directory == "" {
		return nil, nil
	}
	address := os.Getenv("NORI_STATE_EMULATOR_HOST")
	if address == "" {
		address = os.Getenv("FIRESTORE_EMULATOR_HOST")
	}
	if !loopbackEmulator(address) || os.Getenv("TEMPORAL_LOCAL") != "true" {
		return nil, fmt.Errorf("NORI_LOCAL_ARTIFACT_DIR requires a loopback Firestore emulator and TEMPORAL_LOCAL=true")
	}
	if !filepath.IsAbs(directory) {
		return nil, fmt.Errorf("NORI_LOCAL_ARTIFACT_DIR must be an absolute directory")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("NORI_LOCAL_ARTIFACT_DIR must be a directory, not a symbolic link")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	if err := root.Chmod(".", 0700); err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

func loopbackEmulator(address string) bool {
	host, _, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	return err == nil && (host == "localhost" || (ip != nil && ip.IsLoopback()))
}

func (s *Google) Close() error {
	var objectErr, localErr error
	if s.objects != nil {
		objectErr = s.objects.Close()
	}
	if s.localArtifacts != nil {
		localErr = s.localArtifacts.Close()
	}
	return errors.Join(s.db.Close(), objectErr, localErr)
}
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

// Spend records model cost even after a correction or terminal state; it is a
// fact about money already used, not task progress.
func (s *Google) Spend(ctx context.Context, id string, spent, budget float64, costUnknown bool) error {
	return s.db.RunTransaction(ctx, func(ctx context.Context, tx *firestore.Transaction) error {
		r, e := decodeRun(tx.Get(s.runs().Doc(id)))
		if e != nil {
			return e
		}
		// Cumulative projections may be retried or arrive after a newer write.
		return tx.Update(s.runs().Doc(id), []firestore.Update{{Path: "spent_usd", Value: max(r.Spent, spent)}, {Path: "budget_usd", Value: max(r.Budget, budget)}, {Path: "cost_unknown", Value: r.CostUnknown || costUnknown}})
	})
}

// RecordUsage is idempotent for one observed provider request. The caller keeps
// the same ID and timestamp when retrying a failed projection.
func (s *Google) RecordUsage(ctx context.Context, usage Usage) error {
	_, e := s.root().Collection("model_usage").Doc(usage.ID).Set(ctx, usage)
	return e
}

func (s *Google) Artifact(ctx context.Context, id string, a Artifact) error {
	_, e := s.runs().Doc(id).Update(ctx, []firestore.Update{{Path: "artifacts", Value: firestore.ArrayUnion(a)}})
	return e
}
func (s *Google) Finish(ctx context.Context, id string, runStatus RunStatus, detail, text string) (Run, error) {
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
		if runStatus == RunCompleted && r.CancelRequested {
			runStatus = RunCancelled
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
	return s.session(ctx, sessionWithMessages)
}

// ConversationProjection returns runs and usage without messages. Full
// provider turns are selected separately; messages stay cursor-accessible.
func (s *Google) ConversationProjection(ctx context.Context) (Session, error) {
	return s.session(ctx, conversationProjection)
}

type sessionMode int

const (
	sessionWithMessages sessionMode = iota
	conversationProjection
)

func (s *Google) session(ctx context.Context, mode sessionMode) (Session, error) {
	c, e := s.Control(ctx)
	if e != nil {
		return Session{}, e
	}
	session := Session{Messages: []Message{}, Runs: []Run{}, Paused: c.Paused}
	if mode == sessionWithMessages {
		query := s.messages().OrderBy("created_at", firestore.Desc)
		query = query.Limit(100)
		iter := query.Documents(ctx)
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
	// Waiting tasks remain visible even after thirty newer tasks have started.
	open, e := s.OpenTasks(ctx)
	if e != nil {
		return session, e
	}
	seen := map[string]bool{}
	for _, run := range session.Runs {
		seen[run.ID] = true
	}
	for _, run := range open {
		if !seen[run.ID] {
			session.Runs = append(session.Runs, run)
		}
	}
	sort.Slice(session.Runs, func(i, j int) bool { return session.Runs[i].StartedAt.After(session.Runs[j].StartedAt) })
	if mode == conversationProjection {
		usage := s.root().Collection("model_usage").OrderBy("created_at", firestore.Desc).Limit(100).Documents(ctx)
		defer usage.Stop()
		for {
			d, e := usage.Next()
			if e == iterator.Done {
				break
			}
			if e != nil {
				return session, e
			}
			var record Usage
			if e = d.DataTo(&record); e != nil {
				return session, e
			}
			session.Usage = append(session.Usage, record)
		}
	}
	return session, nil
}
func objectPath(run, id string) string { return "artifacts/" + run + "/" + id + ".png" }
func (s *Google) PutPNG(ctx context.Context, run, id string, data []byte) error {
	if err := validateArtifactIDs(run, id); err != nil {
		return err
	}
	if len(data) > 2<<20 {
		return fmt.Errorf("screenshot exceeds 2 MiB")
	}
	if s.localArtifacts != nil {
		return s.putLocalPNG(ctx, run, id, data)
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
	if err := validateArtifactIDs(run, id); err != nil {
		return nil, err
	}
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
	var reader io.ReadCloser
	if s.localArtifacts != nil {
		reader, e = s.localArtifacts.Open(filepath.Join(run, id+".png"))
	} else {
		reader, e = s.objects.Bucket(s.bucket).Object(objectPath(run, id)).NewReader(ctx)
	}
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

func validateArtifactIDs(run, id string) error {
	for _, value := range []string{run, id} {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed.String() != value {
			return fmt.Errorf("artifact run and ID must be canonical UUIDs")
		}
	}
	return nil
}

func (s *Google) putLocalPNG(ctx context.Context, run, id string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.localArtifacts.Mkdir(run, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := s.localArtifacts.Lstat(run)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("artifact task path must be a directory")
	}
	if err := s.localArtifacts.Chmod(run, 0700); err != nil {
		return err
	}
	temporary := filepath.Join(run, "."+uuid.NewString()+".tmp")
	file, err := s.localArtifacts.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer s.localArtifacts.Remove(temporary)
	_, writeErr := file.Write(data)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if err := errors.Join(writeErr, file.Close(), ctx.Err()); err != nil {
		return err
	}
	// Linking commits a complete file atomically and refuses to replace an ID.
	return s.localArtifacts.Link(temporary, filepath.Join(run, id+".png"))
}
