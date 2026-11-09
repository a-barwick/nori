// Package computersession owns who controls the user's computer: the agent (one task at a time) or the user. A
// user takeover fences agent input until the user resumes or the lease expires; resume is validated against the
// lease and the waiting task's instruction revision. Inspecting the session never starts a stopped computer.
package computersession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"nori/internal/computer"
	"nori/internal/permissions"
	"nori/internal/state"
)

const (
	Collection = "computer_sessions"
	SessionID  = "computer"
	// UserLease is how long a takeover lasts without renewal. AgentLease is how long a task keeps the browser
	// between calls.
	UserLease  = 15 * time.Minute
	AgentLease = 5 * time.Minute
)

var (
	ErrUserControl = errors.New("the user is controlling the computer")
	ErrBusy        = errors.New("another task is using the computer")
	ErrLease       = errors.New("this computer lease is not current")
)

type Lease struct {
	ID        string    `json:"id"`
	Holder    string    `json:"holder"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Session is the contract ComputerSession.
type Session struct {
	ID             string     `json:"id"`
	Revision       int64      `json:"revision"`
	TaskID         string     `json:"task_id,omitempty"`
	Availability   string     `json:"availability"`
	Controller     string     `json:"controller"`
	Lease          *Lease     `json:"lease,omitempty"`
	BlockedReason  string     `json:"blocked_reason,omitempty"`
	UpdatedAt      *time.Time `json:"updated_at,omitempty"`
	WaitingTaskIDs []string   `json:"waiting_task_ids,omitempty"`
	TaskTitle      string     `json:"task_title,omitempty"`
}

type stored struct {
	Session
	// Handoffs are takeover interactions waiting for the user to resume, by task.
	Handoffs map[string]string `json:"handoffs,omitempty"`
}

// Service is opened per user.
type Service struct {
	Store       state.ResourceStore
	Runs        state.RunStore
	Provider    computer.Provider
	Permissions *permissions.Service
	Now         func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

func (s *Service) read(ctx context.Context) (stored, int64, error) {
	r, err := s.Store.Resource(ctx, Collection, SessionID)
	if errors.Is(err, state.ErrNotFound) {
		return stored{Session: Session{ID: SessionID, Controller: "none"}}, 0, nil
	}
	if err != nil {
		return stored{}, 0, err
	}
	var doc stored
	if err = r.Decode(&doc); err != nil {
		return stored{}, 0, err
	}
	doc.Revision = r.Revision
	updated := r.UpdatedAt
	doc.UpdatedAt = &updated
	return doc, r.Revision, nil
}

// settle drops expired leases and agent leases held by tasks that are no longer running.
func (s *Service) settle(ctx context.Context, doc *stored) {
	now := s.now()
	if doc.Lease != nil && now.After(doc.Lease.ExpiresAt) {
		doc.Lease, doc.Controller, doc.BlockedReason = nil, "none", ""
	}
	if doc.Lease != nil && doc.Lease.Holder == "agent" && doc.TaskID != "" && s.Runs != nil {
		if run, err := s.Runs.GetRun(ctx, doc.TaskID); err != nil || run.Status != state.RunRunning {
			doc.Lease, doc.Controller, doc.TaskID = nil, "none", ""
		}
	}
	if doc.Controller == "none" {
		doc.TaskID = ""
	}
	waiting := doc.WaitingTaskIDs[:0]
	for _, id := range doc.WaitingTaskIDs {
		if s.Runs == nil {
			break
		}
		if run, err := s.Runs.GetRun(ctx, id); err == nil && run.Status == state.RunRunning && id != doc.TaskID {
			waiting = append(waiting, id)
		}
	}
	doc.WaitingTaskIDs = waiting
}

// Get projects the session. Availability comes from a non-waking inspection: a stopped computer stays stopped.
func (s *Service) Get(ctx context.Context) (Session, error) {
	doc, _, err := s.read(ctx)
	if err != nil {
		return Session{}, err
	}
	s.settle(ctx, &doc)
	doc.Availability = s.availability(ctx, doc)
	if doc.TaskID != "" && s.Runs != nil {
		if run, err := s.Runs.GetRun(ctx, doc.TaskID); err == nil {
			doc.TaskTitle = clip(run.Request, 200)
		}
	}
	return doc.Session, nil
}

func (s *Service) availability(ctx context.Context, doc stored) string {
	if s.Provider == nil {
		return "stopped"
	}
	inspect, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client, err := computer.Attach(inspect, s.Provider, "session-inspect")
	switch {
	case errors.Is(err, computer.ErrStopped):
		return "stopped"
	case err != nil:
		return "failed"
	}
	client.Close()
	if doc.Controller == "agent" {
		return "busy"
	}
	return "ready"
}

func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

func (s *Service) mutate(ctx context.Context, requestID, action string, expected int64, payload any, change func(doc *stored) error) (state.MutationReceipt, error) {
	return s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "POST", Target: "/v1/computer/sessions/" + SessionID + "/actions", ExpectedRevision: expected,
		Payload: payload, Collection: Collection, ID: SessionID,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			doc := stored{Session: Session{ID: SessionID, Controller: "none"}}
			revision := int64(1)
			if current != nil {
				if err := current.Decode(&doc); err != nil {
					return state.MutationResult{}, err
				}
				revision = current.Revision + 1
			}
			s.settle(ctx, &doc)
			if err := change(&doc); err != nil {
				return state.MutationResult{}, err
			}
			doc.Revision = revision
			r, err := state.NewResource(Collection, SessionID, revision, doc, nil, s.now())
			if err != nil {
				return state.MutationResult{}, err
			}
			data, _ := json.Marshal(doc.Session)
			return state.MutationResult{Writes: []state.Resource{r}, Receipt: state.MutationReceipt{Status: "applied", Resource: data, HTTPStatus: 200}}, nil
		},
	})
}

// TakeOver gives the user exclusive control. Agent computer calls stop at the next call; a call already running on
// the computer finishes, and its effects are recorded as usual.
func (s *Service) TakeOver(ctx context.Context, requestID string, expected int64, reason string) (state.MutationReceipt, error) {
	return s.mutate(ctx, requestID, "take_over", expected, map[string]string{"action": "take_over", "reason": reason}, func(doc *stored) error {
		if doc.Controller == "user" {
			return fmt.Errorf("%w: the user already controls the computer", state.ErrInvalidResource)
		}
		if doc.TaskID != "" && !slices.Contains(doc.WaitingTaskIDs, doc.TaskID) {
			doc.WaitingTaskIDs = append(doc.WaitingTaskIDs, doc.TaskID)
		}
		doc.Controller, doc.TaskID, doc.BlockedReason = "user", "", reason
		doc.Lease = &Lease{ID: uuid.NewString(), Holder: "user", ExpiresAt: s.now().Add(UserLease)}
		return nil
	})
}

// Renew extends or Release ends the user's lease. A stale lease ID is rejected.
func (s *Service) Renew(ctx context.Context, requestID string, expected int64, leaseID string, release bool) (state.MutationReceipt, error) {
	action := "renew"
	if release {
		action = "release"
	}
	return s.mutate(ctx, requestID, action, expected, map[string]string{"action": action, "lease_id": leaseID}, func(doc *stored) error {
		if doc.Lease == nil || doc.Lease.Holder != "user" || doc.Lease.ID != leaseID {
			return ErrLease
		}
		if release {
			doc.Lease, doc.Controller, doc.BlockedReason = nil, "none", ""
			return nil
		}
		doc.Lease.ExpiresAt = s.now().Add(UserLease)
		return nil
	})
}

// Resume ends the user's control and lets the named task continue. The lease must be current and the task must
// still be at the instruction revision the user is resuming; a stale lease or revision cannot resume execution.
func (s *Service) Resume(ctx context.Context, requestID string, expected int64, leaseID, taskID string, revision int64) (state.MutationReceipt, string, error) {
	run, err := s.Runs.GetRun(ctx, taskID)
	if err != nil {
		return state.MutationReceipt{}, "", err
	}
	if run.Status != state.RunRunning || run.Revision != revision {
		return state.MutationReceipt{}, "", permissions.ErrInteractionStale
	}
	var interaction string
	receipt, err := s.mutate(ctx, requestID, "resume", expected, map[string]any{"action": "resume", "lease_id": leaseID, "task_id": taskID, "instruction_revision": revision}, func(doc *stored) error {
		if doc.Lease == nil || doc.Lease.Holder != "user" || doc.Lease.ID != leaseID {
			return ErrLease
		}
		interaction = doc.Handoffs[taskID]
		delete(doc.Handoffs, taskID)
		doc.Lease, doc.Controller, doc.BlockedReason = nil, "none", ""
		return nil
	})
	return receipt, interaction, err
}

// Acquire gives a task the computer for its next call. It refuses while the user holds control and while another
// running task holds the agent lease, recording the task as waiting. A task's lease renews with every call.
func (s *Service) Acquire(ctx context.Context, taskID string) error {
	for attempt := 0; attempt < 3; attempt++ {
		doc, revision, err := s.read(ctx)
		if err != nil {
			return err
		}
		s.settle(ctx, &doc)
		switch {
		case doc.Controller == "user":
			if !slices.Contains(doc.WaitingTaskIDs, taskID) {
				doc.WaitingTaskIDs = append(doc.WaitingTaskIDs, taskID)
				_ = s.put(ctx, doc, revision)
			}
			return ErrUserControl
		case doc.Controller == "agent" && doc.TaskID != taskID:
			if !slices.Contains(doc.WaitingTaskIDs, taskID) {
				doc.WaitingTaskIDs = append(doc.WaitingTaskIDs, taskID)
				_ = s.put(ctx, doc, revision)
			}
			return ErrBusy
		}
		doc.Controller, doc.TaskID = "agent", taskID
		if doc.Lease == nil || doc.Lease.Holder != "agent" {
			doc.Lease = &Lease{ID: uuid.NewString(), Holder: "agent"}
		}
		doc.Lease.ExpiresAt = s.now().Add(AgentLease)
		doc.WaitingTaskIDs = slices.DeleteFunc(doc.WaitingTaskIDs, func(id string) bool { return id == taskID })
		if err = s.put(ctx, doc, revision); !errors.Is(err, state.ErrRevisionConflict) {
			return err
		}
	}
	return state.ErrRevisionConflict
}

func (s *Service) put(ctx context.Context, doc stored, expected int64) error {
	r, err := state.NewResource(Collection, SessionID, 0, doc, nil, s.now())
	if err != nil {
		return err
	}
	_, err = s.Store.PutResource(ctx, r, expected)
	return err
}

// Handoff opens (once per task revision and lease) the computer_takeover interaction a task waits on: the user
// signs in or acts on the screen, then resumes the task.
func (s *Service) Handoff(ctx context.Context, taskID string, revision int64, reason, prompt, title string) (permissions.Interaction, error) {
	doc, current, err := s.read(ctx)
	if err != nil {
		return permissions.Interaction{}, err
	}
	s.settle(ctx, &doc)
	lease := ""
	if doc.Lease != nil {
		lease = doc.Lease.ID
	}
	sum := sha256.Sum256(fmt.Appendf(nil, "%s|%d|%s|%s", taskID, revision, reason, lease))
	payload, _ := json.Marshal(map[string]any{"session_id": SessionID, "reason": reason, "lease_id": lease})
	interaction, err := s.Permissions.Open(ctx, permissions.Interaction{
		ID: "int_cmp_" + hex.EncodeToString(sum[:12]), TaskID: taskID, InstructionRevision: &revision, Kind: "computer_takeover",
		Prompt: prompt, Payload: payload, TaskTitle: title,
	}, "")
	if err != nil {
		return interaction, err
	}
	if doc.Handoffs == nil {
		doc.Handoffs = map[string]string{}
	}
	doc.Handoffs[taskID] = interaction.ID
	if !slices.Contains(doc.WaitingTaskIDs, taskID) {
		doc.WaitingTaskIDs = append(doc.WaitingTaskIDs, taskID)
	}
	if reason != "user_requested" {
		doc.BlockedReason = reason
	}
	_ = s.put(ctx, doc, current)
	return interaction, nil
}
