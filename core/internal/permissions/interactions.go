package permissions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"nori/internal/state"
)

const CollectionInteractions = "interactions"

var ErrInteractionStale = errors.New("this question or approval is no longer pending")

type Interaction struct {
	ID                  string          `json:"id"`
	Revision            int64           `json:"revision"`
	TaskID              string          `json:"task_id,omitempty"`
	InstructionRevision *int64          `json:"instruction_revision,omitempty"`
	ConversationID      string          `json:"conversation_id,omitempty"`
	AutomationID        string          `json:"automation_id,omitempty"`
	Kind                string          `json:"kind"`
	Surface             string          `json:"surface,omitempty"`
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
	ConnectionID        string          `json:"-"`
	ToolID              string          `json:"-"`
	ToolTitle           string          `json:"-"`
	ActionClass         string          `json:"-"`
	Recipients          []string        `json:"-"`
}

type Resolution struct {
	Kind         string    `json:"kind"`
	Text         string    `json:"text,omitempty"`
	ConnectionID string    `json:"connection_id,omitempty"`
	At           time.Time `json:"at"`
}

type stored struct {
	Interaction
	ConnectionID    string            `json:"private_connection_id,omitempty"`
	ToolID          string            `json:"private_tool_id,omitempty"`
	ToolTitle       string            `json:"private_tool_title,omitempty"`
	ActionClass     string            `json:"private_action_class,omitempty"`
	Recipients      []string          `json:"private_recipients,omitempty"`
	EditedFields    map[string]string `json:"edited_fields,omitempty"`
	CommandAccepted bool              `json:"command_accepted,omitempty"`
	AnswerText      string            `json:"answer_text,omitempty"`
	AnswerID        string            `json:"answer_command_id,omitempty"`
	RememberDefault bool              `json:"remember_default,omitempty"`
	Purpose         string            `json:"purpose,omitempty"`
}

type Response struct {
	Kind              string            `json:"kind"`
	Text              string            `json:"text,omitempty"`
	OptionIDs         []string          `json:"option_ids,omitempty"`
	ActionDigest      string            `json:"action_digest,omitempty"`
	RememberRule      bool              `json:"remember_rule,omitempty"`
	EditedFields      map[string]string `json:"edited_fields,omitempty"`
	ConnectionID      string            `json:"connection_id,omitempty"`
	RememberAsDefault bool              `json:"remember_as_default,omitempty"`
}

const (
	maxEditedSubjectRunes = 980
	maxEditedBodyRunes    = 12_000
)

// ValidateEditedFields enforces the strict allowlist (only "subject" and "body")
// and field-level safety constraints for editable approval replacements.
func ValidateEditedFields(edited map[string]string) error {
	if len(edited) == 0 {
		return nil
	}
	for k, v := range edited {
		switch k {
		case "subject":
			if strings.ContainsAny(v, "\r\n\x00") {
				return fmt.Errorf("%w: edited subject must not contain newlines or control characters", state.ErrInvalidResource)
			}
			if utf8.RuneCountInString(v) > maxEditedSubjectRunes {
				return fmt.Errorf("%w: edited subject exceeds %d characters", state.ErrInvalidResource, maxEditedSubjectRunes)
			}
			for _, word := range strings.Fields(mime.QEncoding.Encode("UTF-8", v)) {
				if len(word) > maxEditedSubjectRunes {
					return fmt.Errorf("%w: edited subject has an overlong word", state.ErrInvalidResource)
				}
			}
		case "body":
			if strings.ContainsRune(v, '\x00') {
				return fmt.Errorf("%w: edited body must not contain null bytes", state.ErrInvalidResource)
			}
			if utf8.RuneCountInString(v) > maxEditedBodyRunes {
				return fmt.Errorf("%w: edited body exceeds %d characters", state.ErrInvalidResource, maxEditedBodyRunes)
			}
		default:
			return fmt.Errorf("%w: field %q cannot be edited; only subject and body are allowed", state.ErrInvalidResource, k)
		}
	}
	return nil
}

// RecomputeEditedDigest deterministically derives a new action digest bound to
// the original digest, immutable target metadata, and the allowlisted edited fields.
func RecomputeEditedDigest(originalDigest, toolID, connectionID string, recipients []string, edited map[string]string) string {
	recips := append([]string(nil), recipients...)
	sort.Strings(recips)
	keys := make([]string, 0, len(edited))
	for k := range edited {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([][2]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, [2]string{k, edited[k]})
	}
	data, _ := json.Marshal([]any{"edited_v1", originalDigest, toolID, connectionID, recips, pairs})
	sum := sha256.Sum256(data)
	return "act_" + hex.EncodeToString(sum[:20])
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func applyEditedPayload(original json.RawMessage, edited map[string]string) (json.RawMessage, error) {
	payload := map[string]any{}
	if len(original) > 0 {
		if err := json.Unmarshal(original, &payload); err != nil {
			return nil, fmt.Errorf("%w: original interaction payload is invalid", state.ErrInvalidResource)
		}
	}
	editedCopy := cloneStringMap(edited)
	payload["edited_fields"] = editedCopy

	editable, _ := payload["editable_fields"].(map[string]any)
	if editable == nil {
		editable = map[string]any{}
	}
	for k, v := range editedCopy {
		editable[k] = v
	}
	payload["editable_fields"] = editable

	if summary, ok := payload["summary"].(string); ok && summary != "" {
		payload["summary"] = applyEditsToSummary(summary, editedCopy)
	}
	return json.Marshal(payload)
}

func applyEditsToSummary(summary string, edited map[string]string) string {
	updated := summary
	if subj, ok := edited["subject"]; ok {
		lines := strings.Split(updated, "\n")
		replaced := false
		for idx, line := range lines {
			if strings.HasPrefix(line, "Subject: ") {
				lines[idx] = "Subject: " + subj
				replaced = true
				break
			}
		}
		if replaced {
			updated = strings.Join(lines, "\n")
		}
	}
	if body, ok := edited["body"]; ok {
		if header, _, found := strings.Cut(updated, "\n\n"); found && strings.Contains(header, "Subject: ") {
			updated = header + "\n\n" + body
		}
	}
	return updated
}

func replacementInteractionID(originalID, requestID string) string {
	sum := sha256.Sum256([]byte("edit-replacement:" + originalID + "\x00" + requestID))
	return "int_" + hex.EncodeToString(sum[:16])
}

func interactionSurface(kind string, payload json.RawMessage) string {
	if kind != "question" {
		return "chat"
	}
	var q struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal(payload, &q); err == nil {
		switch q.Input {
		case "choice", "multi_choice", "confirm":
			return "chat"
		}
	}
	return "work"
}

func (s *Service) decodeInteraction(r state.Resource) (Interaction, stored, error) {
	var body stored
	if err := r.Decode(&body); err != nil {
		return Interaction{}, body, err
	}
	i := body.Interaction
	i.Revision = r.Revision
	created := r.CreatedAt
	i.CreatedAt = &created
	if i.Payload == nil {
		i.Payload = json.RawMessage("{}")
	}
	i.Surface = interactionSurface(i.Kind, i.Payload)
	if i.Status == "pending" && i.ExpiresAt != nil && s.now().After(*i.ExpiresAt) {
		i.Status = "expired"
	}
	return i, body, nil
}

func (s *Service) Open(ctx context.Context, i Interaction, purpose string) (Interaction, error) {
	if i.Payload == nil {
		i.Payload = json.RawMessage("{}")
	}
	i.Status = "pending"
	body := stored{
		Interaction:  i,
		ConnectionID: i.ConnectionID,
		ToolID:       i.ToolID,
		ToolTitle:    i.ToolTitle,
		ActionClass:  i.ActionClass,
		Recipients:   append([]string(nil), i.Recipients...),
		Purpose:      purpose,
	}
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
	i, _, err := s.decodeInteraction(r)
	return i, err
}

func (s *Service) Interactions(ctx context.Context, status, taskID, after string, limit int) ([]Interaction, string, error) {
	if status != "pending" && status != "expired" {
		page, next, err := s.Store.Resources(ctx, CollectionInteractions, map[string]string{"status": status, "task_id": taskID}, after, limit)
		if err != nil {
			return nil, "", err
		}
		out := make([]Interaction, 0, len(page))
		for _, r := range page {
			i, _, err := s.decodeInteraction(r)
			if err != nil {
				return nil, "", err
			}
			out = append(out, i)
		}
		return out, next, nil
	}
	pageLimit := limit
	if pageLimit <= 0 || pageLimit > 200 {
		pageLimit = 200
	}
	fetchLimit := pageLimit
	if fetchLimit < 50 {
		fetchLimit = 50
	}
	out := make([]Interaction, 0, pageLimit)
	cursor := after
	for {
		page, next, err := s.Store.Resources(ctx, CollectionInteractions, map[string]string{"status": "pending", "task_id": taskID}, cursor, fetchLimit)
		if err != nil {
			return nil, "", err
		}
		for _, r := range page {
			i, _, err := s.decodeInteraction(r)
			if err != nil {
				return nil, "", err
			}
			if i.Status != status {
				continue
			}
			if len(out) == pageLimit {
				return out, out[len(out)-1].ID, nil
			}
			out = append(out, i)
		}
		if next == "" {
			return out, "", nil
		}
		cursor = next
	}
}

type Answer struct {
	CommandID                         string
	TaskID                            string
	Question                          string
	Text                              string
	DefaultPurpose, DefaultConnection string
	Recheck                           bool
	ReplacementInteractionID          string
	ActionDigest                      string
	EditedFields                      map[string]string
}

func (s *Service) Respond(ctx context.Context, id, requestID string, expected int64, response Response) (state.MutationReceipt, *Answer, error) {
	var answer *Answer
	var declined *Interaction
	var replacementForLink *Interaction
	var appliedResource json.RawMessage
	receipt, err := s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "POST", Target: "/v1/interactions/" + id + "/responses", ExpectedRevision: expected,
		Payload: response, Collection: CollectionInteractions, ID: id, SkipRevisionCheck: true,
		Apply: func(current *state.Resource, read func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			answer = nil
			declined = nil
			replacementForLink = nil
			appliedResource = nil
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			i, body, err := s.decodeInteraction(*current)
			if err != nil {
				return state.MutationResult{}, err
			}
			if i.Status != "pending" {
				return state.MutationResult{}, ErrInteractionStale
			}
			if current.Revision != expected {
				return state.MutationResult{}, state.ErrRevisionConflict
			}
			if len(response.EditedFields) > 0 && (response.Kind != "approve" || i.Kind != "approval") {
				return state.MutationResult{}, fmt.Errorf("%w: edited_fields is only allowed when approving an approval interaction", state.ErrInvalidResource)
			}
			now := s.now()
			resolution := Resolution{Kind: response.Kind, At: now}
			text := ""
			var writes []state.Resource
			switch {
			case response.Kind == "decline" && i.Kind == "sign_in":
				if response.RememberRule {
					return state.MutationResult{}, state.ErrInvalidResource
				}
				text = "The user chose not to sign in now. Continue without that account, or report what could not be done."
				resolution.Text = text
			case response.Kind == "signed_in" && i.Kind == "sign_in":
				resolution.Text, text = response.Text, response.Text
			case response.Kind == "answer" && (i.Kind == "question" || i.Kind == "sign_in"):
				if response.RememberRule || response.Text == "" || len(response.Text) > state.MaxMessageBytes {
					return state.MutationResult{}, state.ErrInvalidResource
				}
				resolution.Text, text = response.Text, response.Text
			case (response.Kind == "approve" || response.Kind == "decline") && i.Kind == "approval":
				var payload struct {
					ApprovalOnce bool `json:"approval_once"`
				}
				if err := json.Unmarshal(i.Payload, &payload); err != nil || payload.ApprovalOnce && response.RememberRule {
					return state.MutationResult{}, state.ErrInvalidResource
				}
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
				if len(response.EditedFields) > 0 {
					if response.RememberRule {
						return state.MutationResult{}, fmt.Errorf("%w: remember_rule cannot be combined with edited_fields", state.ErrInvalidResource)
					}
					if err := ValidateEditedFields(response.EditedFields); err != nil {
						return state.MutationResult{}, err
					}
					if s.ValidateEditedPayload != nil {
						if err := s.ValidateEditedPayload(body.ToolID, cloneStringMap(response.EditedFields), i.Payload); err != nil {
							return state.MutationResult{}, fmt.Errorf("%w: %v", state.ErrInvalidResource, err)
						}
					}
					editedCopy := cloneStringMap(response.EditedFields)
					editedPayload, err := applyEditedPayload(i.Payload, editedCopy)
					if err != nil {
						return state.MutationResult{}, err
					}
					newDigest := RecomputeEditedDigest(i.ActionDigest, body.ToolID, body.ConnectionID, body.Recipients, editedCopy)
					replID := replacementInteractionID(id, requestID)

					// Atomically supersede the original interaction and link it to the replacement.
					origInteraction := i
					origInteraction.Status = "superseded"
					origInteraction.SupersededBy = replID
					origInteraction.ResolvedAt = &now
					origBody := body
					origBody.Interaction = origInteraction
					origBody.AnswerID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("interaction-answer:"+requestID)).String()
					text = "The user edited and approved this action; execute using the approved replacement payload (" + replID + "): " + i.Prompt
					origBody.AnswerText = text

					origRes, err := state.NewResource(CollectionInteractions, id, current.Revision+1, origBody, map[string]string{
						"task_id": i.TaskID, "status": "superseded", "kind": i.Kind,
					}, now)
					if err != nil {
						return state.MutationResult{}, err
					}

					// Open & approve the replacement interaction atomically.
					replInteraction := i
					replInteraction.ID = replID
					replInteraction.Revision = 1
					replInteraction.Payload = editedPayload
					replInteraction.ActionDigest = newDigest
					replInteraction.Status = "resolved"
					replInteraction.Resolution = &resolution
					replInteraction.ResolvedAt = &now
					replInteraction.SupersededBy = ""
					replInteraction.Surface = interactionSurface(replInteraction.Kind, replInteraction.Payload)

					replBody := body
					replBody.Interaction = replInteraction
					replBody.EditedFields = editedCopy
					replBody.AnswerID = origBody.AnswerID
					replBody.AnswerText = text

					replRes, err := state.NewResource(CollectionInteractions, replID, 1, replBody, map[string]string{
						"task_id": replInteraction.TaskID, "status": "resolved", "kind": replInteraction.Kind,
					}, now)
					if err != nil {
						return state.MutationResult{}, err
					}

					// Record the approval under the recomputed digest with the approved edited payload.
					record := approval{
						RunID:            i.TaskID,
						Revision:         revision,
						Digest:           newDigest,
						Decision:         "approve",
						Interaction:      replID,
						At:               now,
						EditedFields:     editedCopy,
						EditedPayload:    editedPayload,
						SupersedesDigest: i.ActionDigest,
					}
					approvalKey := ApprovalKey(i.TaskID, revision, newDigest)
					approvalRevision := int64(1)
					if payload.ApprovalOnce {
						previous, err := read(CollectionApprovals, approvalKey)
						if err != nil {
							return state.MutationResult{}, err
						}
						if previous != nil {
							var consumed approval
							if err := previous.Decode(&consumed); err != nil {
								return state.MutationResult{}, err
							}
							if consumed.UsedBy == "" {
								return state.MutationResult{}, ErrInteractionStale
							}
							approvalRevision = previous.Revision + 1
						}
					}
					approvalResource, err := state.NewResource(CollectionApprovals, approvalKey, approvalRevision, record, map[string]string{
						"task_id":           i.TaskID,
						"supersedes_digest": i.ActionDigest,
					}, now)
					if err != nil {
						return state.MutationResult{}, err
					}

					replCopy := replInteraction
					replacementForLink = &replCopy
					data, _ := json.Marshal(replInteraction)
					appliedResource = data
					if i.TaskID != "" {
						answer = &Answer{
							CommandID:                origBody.AnswerID,
							TaskID:                   i.TaskID,
							Question:                 questionID(i),
							Text:                     text,
							ReplacementInteractionID: replID,
							ActionDigest:             newDigest,
							EditedFields:             editedCopy,
						}
					}
					return state.MutationResult{
						Writes:  []state.Resource{origRes, replRes, approvalResource},
						Receipt: state.MutationReceipt{Status: "applied", Resource: data, HTTPStatus: 200},
					}, nil
				}
				record := approval{RunID: i.TaskID, Revision: revision, Digest: i.ActionDigest, Decision: response.Kind, Interaction: i.ID, At: now}
				approvalKey := ApprovalKey(i.TaskID, revision, i.ActionDigest)
				approvalRevision := int64(1)
				if payload.ApprovalOnce {
					previous, err := read(CollectionApprovals, approvalKey)
					if err != nil {
						return state.MutationResult{}, err
					}
					if previous != nil {
						var consumed approval
						if err := previous.Decode(&consumed); err != nil {
							return state.MutationResult{}, err
						}
						if consumed.UsedBy == "" {
							return state.MutationResult{}, ErrInteractionStale
						}
						approvalRevision = previous.Revision + 1
					}
				}
				approvalResource, err := state.NewResource(CollectionApprovals, approvalKey, approvalRevision, record, map[string]string{"task_id": i.TaskID}, now)
				if err != nil {
					return state.MutationResult{}, err
				}
				writes = append(writes, approvalResource)
				text = "The user approved this exact action; retry the same call unchanged: " + i.Prompt
				if response.Kind == "approve" && response.RememberRule {
					ruleResource, err := s.rememberApprovalRule(requestID, i, body)
					if err != nil {
						return state.MutationResult{}, err
					}
					writes = append(writes, ruleResource)
				}
				if response.Kind == "decline" {
					if len(response.Text) > state.MaxMessageBytes {
						return state.MutationResult{}, state.ErrInvalidResource
					}
					prior := i
					prior.ConnectionID = body.ConnectionID
					declined = &prior
					text = "The user declined this action. Do not attempt it; report what was not done: " + i.Prompt
					if note := strings.TrimSpace(response.Text); note != "" {
						resolution.Text = note
						text += " User note: " + note
					}
				}
			case response.Kind == "choose_account" && i.Kind == "account_selection":
				if response.RememberRule {
					return state.MutationResult{}, state.ErrInvalidResource
				}
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
			case (response.Kind == "resume" || response.Kind == "cancel") && i.Kind == "computer_takeover":
				if response.RememberRule || len(response.Text) > state.MaxMessageBytes {
					return state.MutationResult{}, state.ErrInvalidResource
				}
				note := strings.TrimSpace(response.Text)
				if response.Kind == "resume" {
					text = "The user finished taking control of the computer desktop and handed it back to Nori. Continue the task."
				} else {
					text = "The user cancelled the desktop takeover request. Continue without that step or report what could not be done."
				}
				if note != "" {
					resolution.Text = note
					text += " User note: " + note
				} else {
					resolution.Text = text
				}
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
			appliedResource = data
			if i.TaskID != "" {
				answer = &Answer{
					CommandID: body.AnswerID,
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
	transitioned := len(appliedResource) > 0 && len(receipt.Resource) > 0 && &receipt.Resource[0] == &appliedResource[0]
	if !transitioned {
		answer = nil
		declined = nil
		replacementForLink = nil
	}
	if transitioned && replacementForLink != nil && replacementForLink.TaskID != "" {
		s.linkSuperseded(ctx, *replacementForLink)
	}
	if transitioned && declined != nil && s.OnDecline != nil {
		if declineNote := s.OnDecline(ctx, *declined); declineNote != "" {
			if answer != nil {
				answer.Text += declineNote
			}
			if r, readErr := s.Store.Resource(ctx, CollectionInteractions, id); readErr == nil {
				var savedBody stored
				if r.Decode(&savedBody) == nil && !savedBody.CommandAccepted && !strings.HasSuffix(savedBody.AnswerText, declineNote) {
					savedBody.AnswerText += declineNote
					if updated, buildErr := state.NewResource(CollectionInteractions, id, 0, savedBody, r.Index, s.now()); buildErr == nil {
						_, _ = s.Store.PutResource(ctx, updated, r.Revision)
					}
				}
			}
		}
	}
	if answer == nil {
		if r, err := s.Store.Resource(ctx, CollectionInteractions, id); err == nil {
			saved, body, err := s.decodeInteraction(r)
			if err == nil && saved.TaskID != "" && !body.CommandAccepted && body.AnswerText != "" {
				cmdID := body.AnswerID
				if cmdID == "" {
					cmdID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("interaction-answer:"+requestID)).String()
				}
				answer = &Answer{CommandID: cmdID, TaskID: saved.TaskID, Question: questionID(saved), Text: body.AnswerText}
				if saved.SupersededBy != "" {
					answer.ReplacementInteractionID = saved.SupersededBy
					if replRes, replErr := s.Store.Resource(ctx, CollectionInteractions, saved.SupersededBy); replErr == nil {
						if repl, replBody, decErr := s.decodeInteraction(replRes); decErr == nil {
							answer.ActionDigest = repl.ActionDigest
							answer.EditedFields = cloneStringMap(replBody.EditedFields)
						}
					}
				}
				if body.RememberDefault {
					connID := response.ConnectionID
					if saved.Resolution != nil && saved.Resolution.ConnectionID != "" {
						connID = saved.Resolution.ConnectionID
					}
					answer.DefaultPurpose, answer.DefaultConnection = body.Purpose, connID
				}
			}
		}
	}
	return receipt, answer, nil
}

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

func (s *Service) rememberApprovalRule(requestID string, i Interaction, body stored) (state.Resource, error) {
	toolID := strings.TrimSpace(body.ToolID)
	toolTitle := strings.TrimSpace(body.ToolTitle)
	actionClass := strings.TrimSpace(body.ActionClass)
	connID := strings.TrimSpace(body.ConnectionID)
	recipients := body.Recipients

	if toolID == "" || actionClass == "" || (connID == "" && len(recipients) == 0) {
		var payload struct {
			ToolID     string   `json:"tool_id"`
			Effect     string   `json:"effect"`
			Recipients []string `json:"recipients"`
			Account    struct {
				ConnectionID string `json:"connection_id"`
			} `json:"account"`
		}
		if json.Unmarshal(i.Payload, &payload) == nil {
			if toolID == "" {
				toolID = strings.TrimSpace(payload.ToolID)
			}
			if actionClass == "" {
				actionClass = strings.TrimSpace(payload.Effect)
			}
			if connID == "" {
				connID = strings.TrimSpace(payload.Account.ConnectionID)
			}
			if len(recipients) == 0 && len(payload.Recipients) > 0 {
				recipients = payload.Recipients
			}
		}
	}
	if toolID == "" || actionClass == "" {
		return state.Resource{}, fmt.Errorf("%w: approval interaction lacks tool or action class metadata for always-allow rule", state.ErrInvalidResource)
	}
	if toolTitle == "" {
		toolTitle = toolID
	}
	title := "Always allow " + toolTitle
	if len(title) > 200 {
		title = title[:200]
	}
	connSelector := []string{Wildcard}
	if connID != "" {
		connSelector = []string{connID}
	}
	var recipientSelector []string
	if actionClass == "sensitive_send" || len(recipients) > 0 {
		recipientSelector = []string{Wildcard}
	}
	ruleID := "rule_" + strings.ReplaceAll(uuid.NewSHA1(uuid.NameSpaceOID, []byte("rule:"+requestID)).String(), "-", "")[:20]
	res, _, err := s.RuleWrites(ruleID, 1, RuleEditable{
		Effect:        "allow",
		ToolIDs:       []string{toolID},
		ConnectionIDs: connSelector,
		ResourceIDs:   []string{Wildcard},
		ActionClasses: []string{actionClass},
		Recipients:    recipientSelector,
		Title:         title,
	})
	return res, err
}

func (s *Service) MarkDelivered(ctx context.Context, id string) error {
	r, err := s.Store.Resource(ctx, CollectionInteractions, id)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil
		}
		return err
	}
	var body stored
	if err := r.Decode(&body); err != nil {
		return err
	}
	if !body.CommandAccepted {
		body.CommandAccepted = true
		next, err := state.NewResource(CollectionInteractions, id, 0, body, r.Index, s.now())
		if err != nil {
			return err
		}
		if _, err := s.Store.PutResource(ctx, next, r.Revision); err != nil && !errors.Is(err, state.ErrRevisionConflict) {
			return err
		}
	}
	if body.SupersededBy != "" && body.SupersededBy != id {
		_ = s.MarkDelivered(ctx, body.SupersededBy)
	}
	return nil
}

// FindApprovedReplacement looks up an approved edited replacement record for
// (runID, revision, originalDigest) so execution uses the approved replacement payload.
func (s *Service) FindApprovedReplacement(ctx context.Context, runID string, revision int64, originalDigest string) (*ApprovedReplacement, error) {
	if runID == "" || originalDigest == "" {
		return nil, nil
	}
	page, _, err := s.Store.Resources(ctx, CollectionApprovals, map[string]string{
		"task_id":           runID,
		"supersedes_digest": originalDigest,
	}, "", 20)
	if err != nil {
		return nil, err
	}
	var latest *approval
	for _, r := range page {
		var a approval
		if r.Decode(&a) != nil {
			continue
		}
		if a.Revision != revision || a.Decision != "approve" || a.SupersedesDigest != originalDigest {
			continue
		}
		if latest == nil || a.At.After(latest.At) {
			cp := a
			latest = &cp
		}
	}
	if latest == nil {
		return nil, nil
	}
	return &ApprovedReplacement{
		InteractionID:     latest.Interaction,
		OriginalDigest:    latest.SupersedesDigest,
		ReplacementDigest: latest.Digest,
		EditedFields:      cloneStringMap(latest.EditedFields),
		EditedPayload:     append(json.RawMessage(nil), latest.EditedPayload...),
	}, nil
}

// ApprovedReplacementByInteraction resolves an interaction ID (following
// SupersededBy if the original was replaced by an edited approval) and returns
// the replacement record if it was edited and approved.
func (s *Service) ApprovedReplacementByInteraction(ctx context.Context, interactionID string) (*ApprovedReplacement, error) {
	if interactionID == "" {
		return nil, nil
	}
	r, err := s.Store.Resource(ctx, CollectionInteractions, interactionID)
	if err != nil {
		return nil, err
	}
	i, body, err := s.decodeInteraction(r)
	if err != nil {
		return nil, err
	}
	origDigest := i.ActionDigest
	if i.Status == "superseded" && i.SupersededBy != "" {
		replRes, err := s.Store.Resource(ctx, CollectionInteractions, i.SupersededBy)
		if err != nil {
			return nil, err
		}
		i, body, err = s.decodeInteraction(replRes)
		if err != nil {
			return nil, err
		}
	}
	if i.Status != "resolved" || i.Resolution == nil || i.Resolution.Kind != "approve" || len(body.EditedFields) == 0 {
		return nil, nil
	}
	return &ApprovedReplacement{
		InteractionID:     i.ID,
		OriginalDigest:    origDigest,
		ReplacementDigest: i.ActionDigest,
		EditedFields:      cloneStringMap(body.EditedFields),
		EditedPayload:     append(json.RawMessage(nil), i.Payload...),
	}, nil
}

func (s *Service) UnperformedApprovals(ctx context.Context, runID string, revision int64, attempted map[string]bool) ([]string, error) {
	var out []string
	after := ""
	for {
		page, next, err := s.Store.Resources(ctx, CollectionApprovals, map[string]string{"task_id": runID}, after, 200)
		if err != nil {
			return nil, err
		}
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
		if next == "" {
			return out, nil
		}
		after = next
	}
}

func (s *Service) UndeliveredAnswers(ctx context.Context) ([]Answer, []string, error) {
	var answers []Answer
	var ids []string
	after := ""
	for {
		page, next, err := s.Store.Resources(ctx, CollectionInteractions, map[string]string{"status": "resolved"}, after, 200)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range page {
			i, body, err := s.decodeInteraction(r)
			if err != nil || i.TaskID == "" || body.CommandAccepted {
				continue
			}
			if body.AnswerText == "" || body.AnswerID == "" {
				if i.Resolution == nil {
					continue
				}
				body.AnswerID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("interaction-answer-legacy:"+i.ID)).String()
				switch i.Resolution.Kind {
				case "approve":
					body.AnswerText = "The user approved this exact action; retry the same call unchanged: " + i.Prompt
				case "decline":
					body.AnswerText = "The user declined this action. Do not attempt it; report what was not done: " + i.Prompt
					if note := strings.TrimSpace(i.Resolution.Text); note != "" {
						body.AnswerText += " User note: " + note
					}
				default:
					body.AnswerText = i.Resolution.Text
				}
				if body.AnswerText == "" {
					continue
				}
			}
			answer := Answer{CommandID: body.AnswerID, TaskID: i.TaskID, Question: questionID(i), Text: body.AnswerText}
			if body.RememberDefault && i.Resolution != nil {
				answer.DefaultPurpose, answer.DefaultConnection = body.Purpose, i.Resolution.ConnectionID
			}
			answers = append(answers, answer)
			ids = append(ids, i.ID)
		}
		if next == "" {
			return answers, ids, nil
		}
		after = next
	}
}
