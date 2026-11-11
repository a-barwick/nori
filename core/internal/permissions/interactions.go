package permissions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/state"
)

// CollectionInteractions matches records.CollectionInteractions, where task corrections supersede pending ones.
const CollectionInteractions = "interactions"

var ErrInteractionStale = errors.New("this question or approval is no longer pending")

// Interaction is the contract Interaction.
type Interaction struct {
	ID                  string          `json:"id"`
	Revision            int64           `json:"revision"`
	TaskID              string          `json:"task_id,omitempty"`
	InstructionRevision *int64          `json:"instruction_revision,omitempty"`
	ConversationID      string          `json:"conversation_id,omitempty"`
	AutomationID        string          `json:"automation_id,omitempty"`
	Kind                string          `json:"kind"`
	Status              string          `json:"status"`
	Prompt              string          `json:"prompt"`
	Payload             json.RawMessage `json:"payload"`
	ActionDigest        string          `json:"action_digest,omitempty"`
	ExpiresAt           *time.Time      `json:"expires_at,omitempty"`
	CreatedAt           *time.Time      `json:"created_at,omitempty"`
	ResolvedAt          *time.Time      `json:"resolved_at,omitempty"`
	LegacyQuestionID    string          `json:"legacy_question_id,omitempty"`
	SupersededBy        string          `json:"superseded_by,omitempty"`
	TaskTitle           string          `json:"task_title,omitempty"`
	Resolution          *Resolution     `json:"resolution,omitempty"`
	// Private fields the executor needs when the call is retried; never projected.
	ConnectionID string `json:"-"`
}

type Resolution struct {
	Kind         string    `json:"kind"`
	Text         string    `json:"text,omitempty"`
	ConnectionID string    `json:"connection_id,omitempty"`
	At           time.Time `json:"at"`
}

type stored struct {
	Interaction
	ConnectionID    string `json:"private_connection_id,omitempty"`
	CommandAccepted bool   `json:"command_accepted,omitempty"`
	// AnswerText is the exact answer command text, so a retried response delivers the identical command.
	AnswerText      string `json:"answer_text,omitempty"`
	AnswerID        string `json:"answer_command_id,omitempty"`
	RememberDefault bool   `json:"remember_default,omitempty"`
	Purpose         string `json:"purpose,omitempty"`
}

// Response is the contract InteractionResponse value.
type Response struct {
	Kind              string   `json:"kind"`
	Text              string   `json:"text,omitempty"`
	OptionIDs         []string `json:"option_ids,omitempty"`
	ActionDigest      string   `json:"action_digest,omitempty"`
	ConnectionID      string   `json:"connection_id,omitempty"`
	RememberAsDefault bool     `json:"remember_as_default,omitempty"`
	SessionID         string   `json:"session_id,omitempty"`
	LeaseID           string   `json:"lease_id,omitempty"`
}

func decodeInteraction(r state.Resource) (Interaction, stored, error) {
	var s stored
	if err := r.Decode(&s); err != nil {
		return Interaction{}, s, err
	}
	i := s.Interaction
	i.Revision = r.Revision
	created := r.CreatedAt
	i.CreatedAt = &created
	if i.Payload == nil {
		i.Payload = json.RawMessage("{}")
	}
	if i.Status == "pending" && i.ExpiresAt != nil && time.Now().After(*i.ExpiresAt) {
		i.Status = "expired"
	}
	return i, s, nil
}

// Open creates an interaction once; a repeated call with the same ID returns the stored one.
func (s *Service) Open(ctx context.Context, i Interaction, purpose string) (Interaction, error) {
	if i.Payload == nil {
		i.Payload = json.RawMessage("{}")
	}
	i.Status = "pending"
	body := stored{Interaction: i, ConnectionID: i.ConnectionID, Purpose: purpose}
	r, err := state.NewResource(CollectionInteractions, i.ID, 0, body, map[string]string{"task_id": i.TaskID, "status": "pending", "kind": i.Kind}, s.now())
	if err != nil {
		return Interaction{}, err
	}
	created, err := s.Store.PutResource(ctx, r, 0)
	if err != nil && !errors.Is(err, state.ErrRevisionConflict) {
		return Interaction{}, err
	}
	if err == nil && created.Revision == 1 && i.TaskID != "" {
		s.linkSuperseded(ctx, i)
	}
	return s.Interaction(ctx, i.ID)
}

// linkSuperseded points interactions of the same task and kind that a correction superseded at the new one, so a
// client holding a stale card can jump to its replacement.
func (s *Service) linkSuperseded(ctx context.Context, replacement Interaction) {
	page, _, err := s.Store.Resources(ctx, CollectionInteractions, map[string]string{"task_id": replacement.TaskID, "status": "superseded"}, "", 50)
	if err != nil {
		return
	}
	for _, r := range page {
		var body map[string]any
		if r.Decode(&body) != nil || body["kind"] != replacement.Kind {
			continue
		}
		if linked, _ := body["superseded_by"].(string); strings.HasPrefix(linked, "int_") || linked == replacement.ID {
			continue
		}
		body["superseded_by"] = replacement.ID
		next, err := state.NewResource(CollectionInteractions, r.ID, 0, body, r.Index, s.now())
		if err == nil {
			_, _ = s.Store.PutResource(ctx, next, r.Revision)
		}
	}
}

func (s *Service) Interaction(ctx context.Context, id string) (Interaction, error) {
	r, err := s.Store.Resource(ctx, CollectionInteractions, id)
	if err != nil {
		return Interaction{}, err
	}
	i, _, err := decodeInteraction(r)
	return i, err
}

// Interactions lists interactions, optionally by status and task.
func (s *Service) Interactions(ctx context.Context, status, taskID, after string, limit int) ([]Interaction, string, error) {
	page, next, err := s.Store.Resources(ctx, CollectionInteractions, map[string]string{"status": status, "task_id": taskID}, after, limit)
	if err != nil {
		return nil, "", err
	}
	out := make([]Interaction, 0, len(page))
	for _, r := range page {
		i, _, err := decodeInteraction(r)
		if err != nil {
			return nil, "", err
		}
		out = append(out, i)
	}
	return out, next, nil
}

// Answer is what resumes the waiting task: an answer command for its question.
type Answer struct {
	CommandID string
	TaskID    string
	Question  string
	Text      string
	// Default is set when the user chose an account and asked to remember it for the purpose.
	DefaultPurpose, DefaultConnection string
	// Recheck marks an answer recorded as delivered: deliver it again only if its task is still waiting on exactly
	// this question, which proves the earlier delivery never landed.
	Recheck bool
}

// Respond resolves a pending interaction. Resolution, the approval record and the answer intent commit together;
// a stale revision, changed action digest, resolved or expired interaction is rejected with the current state.
func (s *Service) Respond(ctx context.Context, id, requestID string, expected int64, response Response) (state.MutationReceipt, *Answer, error) {
	var answer *Answer
	declineNote := ""
	if response.Kind == "decline" && s.OnDecline != nil {
		// Provider clean-up can't run inside the transaction; it runs first, only for the exact action being declined.
		if i, err := s.Interaction(ctx, id); err == nil && i.Kind == "approval" && i.Status == "pending" && response.ActionDigest != "" && response.ActionDigest == i.ActionDigest {
			declineNote = s.OnDecline(ctx, i)
		}
	}
	receipt, err := s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "POST", Target: "/v1/interactions/" + id + "/responses", ExpectedRevision: expected,
		Payload: response, Collection: CollectionInteractions, ID: id, SkipRevisionCheck: true,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			i, body, err := decodeInteraction(*current)
			if err != nil {
				return state.MutationResult{}, err
			}
			// A resolved, expired or superseded interaction is stale whatever revision the client read.
			if i.Status != "pending" {
				return state.MutationResult{}, ErrInteractionStale
			}
			if current.Revision != expected {
				return state.MutationResult{}, state.ErrRevisionConflict
			}
			now := s.now()
			resolution := Resolution{Kind: response.Kind, At: now}
			text := ""
			var writes []state.Resource
			switch {
			case response.Kind == "decline" && i.Kind == "sign_in":
				// "Not now": the task carries on without that account or reports what it could not do.
				text = "The user chose not to sign in now. Continue without that account, or report what could not be done."
				resolution.Text = text
			case response.Kind == "signed_in" && i.Kind == "sign_in":
				// Server-side: the connection became usable again (any sign-in or repair of it, from the task or Apps).
				resolution.Text, text = response.Text, response.Text
			case response.Kind == "answer" && (i.Kind == "question" || i.Kind == "sign_in"):
				if response.Text == "" || len(response.Text) > state.MaxMessageBytes {
					return state.MutationResult{}, state.ErrInvalidResource
				}
				resolution.Text, text = response.Text, response.Text
			case (response.Kind == "approve" || response.Kind == "decline") && i.Kind == "approval":
				// A missing digest is a malformed request; a different one means the action changed since it was shown.
				if response.ActionDigest == "" {
					return state.MutationResult{}, fmt.Errorf("%w: approve and decline need the action_digest that was shown", state.ErrInvalidResource)
				}
				if response.ActionDigest != i.ActionDigest {
					return state.MutationResult{}, ErrInteractionStale
				}
				revision := int64(0)
				if i.InstructionRevision != nil {
					revision = *i.InstructionRevision
				}
				record := approval{RunID: i.TaskID, Revision: revision, Digest: i.ActionDigest, Decision: response.Kind, Interaction: i.ID, At: now}
				approvalResource, err := state.NewResource(CollectionApprovals, ApprovalKey(i.TaskID, revision, i.ActionDigest), 1, record, map[string]string{"task_id": i.TaskID}, now)
				if err != nil {
					return state.MutationResult{}, err
				}
				writes = append(writes, approvalResource)
				text = "The user approved this exact action; retry the same call unchanged: " + i.Prompt
				if response.Kind == "decline" {
					text = "The user declined this action. Do not attempt it; report what was not done: " + i.Prompt + declineNote
				}
			case response.Kind == "choose_account" && i.Kind == "account_selection":
				var payload struct {
					Purpose  string   `json:"purpose"`
					Eligible []string `json:"eligible_connection_ids"`
				}
				_ = json.Unmarshal(i.Payload, &payload)
				if !contains(payload.Eligible, response.ConnectionID) {
					return state.MutationResult{}, state.ErrInvalidResource
				}
				resolution.ConnectionID = response.ConnectionID
				text = fmt.Sprintf("The user chose the account %s. Retry the call with account set to %s.", response.ConnectionID, response.ConnectionID)
				if response.RememberAsDefault {
					body.RememberDefault = true
				}
			case response.Kind == "resume_computer" && i.Kind == "computer_takeover":
				// The computer session validated the lease and task revision before this response.
				text = "The user finished controlling the computer. Check the browser's current state before continuing."
			default:
				return state.MutationResult{}, state.ErrInvalidResource
			}
			i.Status, i.Resolution, i.ResolvedAt = "resolved", &resolution, &now
			body.Interaction = i
			body.AnswerText = text
			body.AnswerID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("interaction-answer:"+requestID)).String()
			next, err := state.NewResource(CollectionInteractions, id, current.Revision+1, body, map[string]string{"task_id": i.TaskID, "status": "resolved", "kind": i.Kind}, now)
			if err != nil {
				return state.MutationResult{}, err
			}
			i.Revision = current.Revision + 1
			data, _ := json.Marshal(i)
			if i.TaskID != "" {
				answer = &Answer{
					CommandID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("interaction-answer:"+requestID)).String(),
					TaskID:    i.TaskID, Question: questionID(i), Text: text,
				}
				if body.RememberDefault {
					answer.DefaultPurpose, answer.DefaultConnection = body.Purpose, response.ConnectionID
				}
			}
			return state.MutationResult{Writes: append([]state.Resource{next}, writes...),
				Receipt: state.MutationReceipt{Status: "applied", Resource: data, HTTPStatus: 200}}, nil
		},
	})
	if err != nil {
		return receipt, nil, err
	}
	if answer == nil {
		// A retried request: rebuild the same answer intent so delivery can be repeated idempotently.
		if r, err := s.Store.Resource(ctx, CollectionInteractions, id); err == nil {
			saved, body, err := decodeInteraction(r)
			if err == nil && saved.TaskID != "" && !body.CommandAccepted && body.AnswerText != "" {
				answer = &Answer{CommandID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("interaction-answer:"+requestID)).String(),
					TaskID: saved.TaskID, Question: questionID(saved), Text: body.AnswerText}
			}
		}
	}
	return receipt, answer, nil
}

// questionID is the task question the answer resolves: the legacy question it projects, or the interaction itself.
func questionID(i Interaction) string {
	if i.LegacyQuestionID != "" {
		return i.LegacyQuestionID
	}
	return i.ID
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

// MarkDelivered records that the answer command was accepted, so repair does not repeat it.
func (s *Service) MarkDelivered(ctx context.Context, id string) {
	r, err := s.Store.Resource(ctx, CollectionInteractions, id)
	if err != nil {
		return
	}
	var body stored
	if r.Decode(&body) != nil || body.CommandAccepted {
		return
	}
	body.CommandAccepted = true
	next, err := state.NewResource(CollectionInteractions, id, 0, body, r.Index, s.now())
	if err == nil {
		_, _ = s.Store.PutResource(ctx, next, r.Revision)
	}
}

// UnperformedApprovals lists actions the user approved for a task revision that no admitted operation attempted,
// so a task cannot finish as if an approved action had happened.
func (s *Service) UnperformedApprovals(ctx context.Context, runID string, revision int64, attempted map[string]bool) ([]string, error) {
	page, _, err := s.Store.Resources(ctx, CollectionApprovals, map[string]string{"task_id": runID}, "", 200)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range page {
		var a approval
		if r.Decode(&a) != nil || a.Decision != "approve" || a.Revision != revision || attempted[a.Digest] {
			continue
		}
		summary := "an approved action"
		if i, err := s.Interaction(ctx, a.Interaction); err == nil {
			summary = i.Prompt
		}
		out = append(out, summary)
	}
	return out, nil
}

// UndeliveredAnswers lists resolved interactions whose answer command has not been accepted yet, for example
// because the user answered before the task published its question. Delivery is retried until it is accepted.
func (s *Service) UndeliveredAnswers(ctx context.Context) ([]Answer, []string, error) {
	page, _, err := s.Store.Resources(ctx, CollectionInteractions, map[string]string{"status": "resolved"}, "", 200)
	if err != nil {
		return nil, nil, err
	}
	var answers []Answer
	var ids []string
	for _, r := range page {
		i, body, err := decodeInteraction(r)
		if err != nil || i.TaskID == "" {
			continue
		}
		if body.AnswerText == "" || body.AnswerID == "" {
			// Resolved before answers were saved with the resolution: rebuild the same answer from the resolution.
			if i.Resolution == nil {
				continue
			}
			body.AnswerID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("interaction-answer-legacy:"+i.ID)).String()
			switch i.Resolution.Kind {
			case "approve":
				body.AnswerText = "The user approved this exact action; retry the same call unchanged: " + i.Prompt
			case "decline":
				body.AnswerText = "The user declined this action. Do not attempt it; report what was not done: " + i.Prompt
			default:
				body.AnswerText = i.Resolution.Text
			}
			if body.AnswerText == "" {
				continue
			}
		}
		answers = append(answers, Answer{CommandID: body.AnswerID, TaskID: i.TaskID, Question: questionID(i), Text: body.AnswerText, Recheck: body.CommandAccepted})
		ids = append(ids, i.ID)
	}
	return answers, ids, nil
}
