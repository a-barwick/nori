package permissions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/state"
)

const (
	CollectionRules     = "permission_rules"
	CollectionApprovals = "approvals"
	CollectionAuthority = "authority"
)

type Rule struct {
	ID            string     `json:"id"`
	Revision      int64      `json:"revision"`
	Effect        string     `json:"effect"`
	ToolIDs       []string   `json:"tool_ids"`
	ConnectionIDs []string   `json:"connection_ids"`
	ResourceIDs   []string   `json:"resource_ids"`
	ActionClasses []string   `json:"action_classes"`
	Recipients    []string   `json:"recipients,omitempty"`
	AutomationID  string     `json:"automation_id,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	MaxOperations int        `json:"max_operations,omitempty"`
	Title         string     `json:"title,omitempty"`
	Summary       string     `json:"summary,omitempty"`
	CreatedAt     *time.Time `json:"created_at,omitempty"`
	UpdatedAt     *time.Time `json:"updated_at,omitempty"`
	Used          int        `json:"-"`
}

type RuleEditable struct {
	Effect        string     `json:"effect"`
	ToolIDs       []string   `json:"tool_ids"`
	ConnectionIDs []string   `json:"connection_ids"`
	ResourceIDs   []string   `json:"resource_ids"`
	ActionClasses []string   `json:"action_classes"`
	Recipients    []string   `json:"recipients,omitempty"`
	AutomationID  string     `json:"automation_id,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	MaxOperations int        `json:"max_operations,omitempty"`
	Title         string     `json:"title,omitempty"`
}

type storedRule struct {
	Rule
	Used int `json:"used"`
}

const Wildcard = "*"

func validEffects(classes []string) bool {
	for _, class := range classes {
		switch class {
		case "read", "reversible_write", "sensitive_send", "unknown":
		default:
			return false
		}
	}
	return true
}

func Summarize(r RuleEditable) string {
	verb := "Allow"
	switch r.Effect {
	case "deny":
		verb = "Never allow"
	case "ask":
		verb = "Ask before"
	}
	list := func(values []string, all, none string) string {
		if slices.Contains(values, Wildcard) {
			return all
		}
		if len(values) == 0 {
			return none
		}
		return strings.Join(values, ", ")
	}
	classes := map[string]string{"read": "reads", "reversible_write": "changes", "sensitive_send": "sends", "unknown": "unclassified actions"}
	var actions []string
	for _, class := range r.ActionClasses {
		actions = append(actions, classes[class])
	}
	text := fmt.Sprintf("%s %s with %s on %s", verb, strings.Join(actions, " and "),
		list(r.ToolIDs, "any tool", "no tools"), list(r.ConnectionIDs, "any connected account", "no accounts"))
	if len(r.ResourceIDs) > 0 {
		text += ", limited to " + list(r.ResourceIDs, "any resource", "")
	}
	if len(r.Recipients) > 0 {
		text += ", to " + list(r.Recipients, "anyone", "")
	}
	if r.AutomationID != "" {
		text += ", only for this automation"
	}
	if r.MaxOperations > 0 {
		text += fmt.Sprintf(", at most %d times", r.MaxOperations)
	}
	if r.ExpiresAt != nil {
		text += ", until " + r.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return text + "."
}

type Service struct {
	Store            state.ResourceStore
	UnattendedWrites string
	UserSends        string
	Now              func() time.Time
	OnDecline        func(ctx context.Context, i Interaction) string
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

func (s *Service) Rules(ctx context.Context) ([]Rule, error) {
	var out []Rule
	after := ""
	for {
		page, next, err := s.Store.Resources(ctx, CollectionRules, nil, after, 200)
		if err != nil {
			return nil, err
		}
		for _, r := range page {
			rule, err := decodeRule(r)
			if err != nil {
				return nil, err
			}
			out = append(out, rule)
		}
		if next == "" {
			return out, nil
		}
		after = next
	}
}

func decodeRule(r state.Resource) (Rule, error) {
	var stored storedRule
	if err := r.Decode(&stored); err != nil {
		return Rule{}, err
	}
	rule := stored.Rule
	rule.Revision, rule.Used = r.Revision, stored.Used
	created, updated := r.CreatedAt, r.UpdatedAt
	rule.CreatedAt, rule.UpdatedAt = &created, &updated
	return rule, nil
}

func (s *Service) Rule(ctx context.Context, id string) (Rule, error) {
	r, err := s.Store.Resource(ctx, CollectionRules, id)
	if err != nil {
		return Rule{}, err
	}
	return decodeRule(r)
}

func validRule(value RuleEditable) error {
	if (value.Effect != "allow" && value.Effect != "ask" && value.Effect != "deny") || !validEffects(value.ActionClasses) || len(value.ActionClasses) == 0 {
		return state.ErrInvalidResource
	}
	if value.MaxOperations < 0 {
		return state.ErrInvalidResource
	}
	if len(value.ToolIDs)+len(value.ConnectionIDs)+len(value.ResourceIDs)+len(value.Recipients) > 512 || len(value.Title) > 200 {
		return state.ErrInvalidResource
	}
	return nil
}

func (s *Service) SaveRule(ctx context.Context, id, requestID string, expected int64, value RuleEditable) (state.MutationReceipt, error) {
	if err := validRule(value); err != nil {
		return state.MutationReceipt{}, err
	}
	method, target, status := "PUT", "/v1/permissions/"+id, 200
	if id == "" {
		id = "rule_" + strings.ReplaceAll(uuid.NewSHA1(uuid.NameSpaceOID, []byte("rule:"+requestID)).String(), "-", "")[:20]
		method, target, status = "POST", "/v1/permissions", 201
	}
	return s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: method, Target: target, ExpectedRevision: expected, Payload: value,
		Collection: CollectionRules, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			revision := int64(1)
			used := 0
			if current != nil {
				revision = current.Revision + 1
				var prev storedRule
				if err := current.Decode(&prev); err != nil {
					return state.MutationResult{}, err
				}
				used = prev.Used
			}
			result, err := s.ruleWrite(id, revision, used, value)
			result.Receipt.HTTPStatus = status
			return result, err
		},
	})
}

func (s *Service) ruleWrite(id string, revision int64, used int, value RuleEditable) (state.MutationResult, error) {
	rule := Rule{
		ID: id, Revision: revision, Effect: value.Effect, ToolIDs: nonNil(value.ToolIDs), ConnectionIDs: nonNil(value.ConnectionIDs),
		ResourceIDs: nonNil(value.ResourceIDs), ActionClasses: nonNil(value.ActionClasses), Recipients: value.Recipients,
		AutomationID: value.AutomationID, ExpiresAt: value.ExpiresAt, MaxOperations: value.MaxOperations,
		Title: value.Title, Summary: Summarize(value),
	}
	now := s.now()
	r, err := state.NewResource(CollectionRules, id, revision, storedRule{Rule: rule, Used: used}, map[string]string{"automation_id": value.AutomationID}, now)
	if err != nil {
		return state.MutationResult{}, err
	}
	rule.UpdatedAt = &now
	data, _ := json.Marshal(rule)
	return state.MutationResult{Writes: []state.Resource{r}, Receipt: state.MutationReceipt{Status: "applied", Resource: data, HTTPStatus: 200}}, nil
}

func (s *Service) RuleWrites(id string, revision int64, value RuleEditable) (state.Resource, Rule, error) {
	if err := validRule(value); err != nil {
		return state.Resource{}, Rule{}, err
	}
	result, err := s.ruleWrite(id, revision, 0, value)
	if err != nil {
		return state.Resource{}, Rule{}, err
	}
	var rule Rule
	_ = json.Unmarshal(result.Receipt.Resource, &rule)
	return result.Writes[0], rule, nil
}

func (s *Service) RevokeRule(ctx context.Context, id, requestID string, expected int64) (state.MutationReceipt, error) {
	return s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "POST", Target: "/v1/permissions/" + id + "/actions", ExpectedRevision: expected,
		Payload: map[string]string{"action": "revoke"}, Collection: CollectionRules, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			rule, err := decodeRule(*current)
			if err != nil {
				return state.MutationResult{}, err
			}
			data, err := json.Marshal(rule)
			if err != nil {
				return state.MutationResult{}, err
			}
			return state.MutationResult{Deletes: []state.ResourceKey{{Collection: CollectionRules, ID: id}},
				Receipt: state.MutationReceipt{Status: "applied", Resource: data, HTTPStatus: 200}}, nil
		},
	})
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

type Request struct {
	RunID                   string
	Revision                int64
	Origin                  string
	AutomationID            string
	ReadOnly                bool
	ToolID                  string
	Effect                  string
	ConnectionID            string
	Epoch                   int64
	ResourceIDs             []string
	Recipients              []string
	Digest                  string
	RequireExplicitApproval bool
	OperationID             string
}

type Decision struct {
	Result          string
	Reason          string
	Source          string
	AuthorityRef    string
	RuleIDs         []string
	RuleSummaries   []string
	ApprovedByHuman bool
}

func matches(selector []string, value string) bool {
	return slices.Contains(selector, Wildcard) || slices.Contains(selector, value)
}

func matchesFold(selector []string, value string) bool {
	for _, s := range selector {
		if s == Wildcard || strings.EqualFold(s, value) {
			return true
		}
	}
	return false
}

func covers(selector, values []string) bool {
	for _, value := range values {
		if !matches(selector, value) {
			return false
		}
	}
	return true
}

func coversFold(selector, values []string) bool {
	for _, value := range values {
		if !matchesFold(selector, value) {
			return false
		}
	}
	return true
}

func mcpServerID(toolID string) string {
	if !strings.HasPrefix(toolID, "mcp/") {
		return ""
	}
	rest := strings.TrimPrefix(toolID, "mcp/")
	server, _, ok := strings.Cut(rest, "/")
	if !ok || server == "" {
		return ""
	}
	return server
}

func (s *Service) applies(rule Rule, req Request) bool {
	if rule.ExpiresAt != nil && s.now().After(*rule.ExpiresAt) {
		return false
	}
	if rule.MaxOperations > 0 && rule.Effect == "allow" && rule.Used >= rule.MaxOperations {
		return false
	}
	if rule.AutomationID != "" && rule.AutomationID != req.AutomationID {
		return false
	}
	if !matches(rule.ActionClasses, req.Effect) || !matches(rule.ToolIDs, req.ToolID) {
		return false
	}
	if req.ConnectionID != "" {
		if !matches(rule.ConnectionIDs, req.ConnectionID) {
			return false
		}
	} else if len(rule.ConnectionIDs) > 0 {
		serverID := mcpServerID(req.ToolID)
		if !matches(rule.ConnectionIDs, "") && (serverID == "" || !matchesFold(rule.ConnectionIDs, serverID)) {
			return false
		}
	}
	if rule.Effect == "allow" {
		if len(rule.ResourceIDs) > 0 && !slices.Contains(rule.ResourceIDs, Wildcard) && len(req.ResourceIDs) == 0 {
			return false
		}
		if len(req.ResourceIDs) > 0 && !covers(rule.ResourceIDs, req.ResourceIDs) {
			return false
		}
		if len(rule.Recipients) > 0 && !slices.Contains(rule.Recipients, Wildcard) && len(req.Recipients) == 0 {
			return false
		}
		if len(req.Recipients) > 0 && !coversFold(rule.Recipients, req.Recipients) {
			return false
		}
	}
	if rule.Effect == "deny" || rule.Effect == "ask" {
		if len(rule.ResourceIDs) > 0 && !slices.Contains(rule.ResourceIDs, Wildcard) && !intersects(rule.ResourceIDs, req.ResourceIDs) {
			return false
		}
		if len(rule.Recipients) > 0 && !slices.Contains(rule.Recipients, Wildcard) && !intersectsFold(rule.Recipients, req.Recipients) {
			return false
		}
	}
	return true
}

func intersects(selector, values []string) bool {
	for _, value := range values {
		if matches(selector, value) {
			return true
		}
	}
	return false
}

func intersectsFold(selector, values []string) bool {
	for _, value := range values {
		if matchesFold(selector, value) {
			return true
		}
	}
	return false
}

func ApprovalKey(runID string, revision int64, digest string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%s", runID, revision, digest)))
	return "apv_" + hex.EncodeToString(sum[:20])
}

type approval struct {
	RunID       string    `json:"run_id"`
	Revision    int64     `json:"revision"`
	Digest      string    `json:"digest"`
	Decision    string    `json:"decision"`
	Interaction string    `json:"interaction_id"`
	At          time.Time `json:"at"`
	UsedBy      string    `json:"used_by,omitempty"`
}

func (s *Service) Check(ctx context.Context, req Request) (Decision, error) {
	if req.ReadOnly && req.Effect != "read" {
		return Decision{Result: "deny", Reason: "This task may only read."}, nil
	}
	rules, err := s.Rules(ctx)
	if err != nil {
		return Decision{}, err
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })
	for _, rule := range rules {
		if rule.Effect == "deny" && s.applies(rule, req) {
			return Decision{Result: "deny", Reason: "A permission rule forbids this: " + rule.Summary, RuleIDs: []string{rule.ID}, RuleSummaries: []string{rule.Summary}}, nil
		}
	}
	if req.Digest != "" && req.RunID != "" {
		if r, err := s.Store.Resource(ctx, CollectionApprovals, ApprovalKey(req.RunID, req.Revision, req.Digest)); err == nil {
			var a approval
			if err := r.Decode(&a); err != nil {
				return Decision{}, err
			}
			if a.Decision == "decline" {
				return Decision{Result: "deny", Reason: "The user declined this exact action."}, nil
			}
			if req.RequireExplicitApproval {
				used, err := s.useApproval(ctx, r, a, req.OperationID)
				if err != nil {
					return Decision{}, err
				}
				if !used {
					return Decision{Result: "needs_interaction", Reason: "Approve this action again before repeating it."}, nil
				}
			}
			dec, err := s.allow(ctx, req, "approval", nil, a.Interaction)
			if err != nil {
				return Decision{}, err
			}
			dec.ApprovedByHuman = true
			return dec, nil
		} else if !errors.Is(err, state.ErrNotFound) {
			return Decision{}, err
		}
	}
	if req.RequireExplicitApproval {
		return Decision{Result: "needs_interaction", Reason: "Approve this action before it runs."}, nil
	}
	for _, rule := range rules {
		if rule.Effect == "ask" && s.applies(rule, req) {
			return Decision{Result: "needs_interaction", Reason: "A custom rule requires your approval: " + rule.Summary, RuleIDs: []string{rule.ID}, RuleSummaries: []string{rule.Summary}}, nil
		}
	}
	for _, rule := range rules {
		if rule.Effect == "allow" && s.applies(rule, req) {
			if err := s.consume(ctx, rule); err != nil {
				if errors.Is(err, errRuleExhausted) || errors.Is(err, state.ErrNotFound) {
					continue
				}
				return Decision{}, err
			}
			dec, err := s.allow(ctx, req, "permission_rule", []string{rule.ID}, "")
			if err != nil {
				return Decision{}, err
			}
			dec.RuleSummaries = []string{rule.Summary}
			return dec, nil
		}
	}
	switch {
	case req.Effect == "read" || req.Effect == "control":
		return s.allow(ctx, req, originSource(req.Origin), nil, "")
	case req.Origin == "user" && req.Effect == "reversible_write":
		return s.allow(ctx, req, "user_request", nil, "")
	case req.Origin == "user" && req.Effect == "sensitive_send" && s.UserSends == "allow":
		return s.allow(ctx, req, "user_request", nil, "")
	case req.Origin != "user" && s.UnattendedWrites == "require_rule":
		return Decision{Result: "deny", Reason: "This automation has no standing permission for this action."}, nil
	}
	return Decision{Result: "needs_interaction", Reason: "This action needs the user's approval."}, nil
}

func (s *Service) useApproval(ctx context.Context, r state.Resource, stored approval, operationID string) (bool, error) {
	if operationID == "" {
		return false, state.ErrInvalidResource
	}
	if stored.UsedBy != "" {
		return stored.UsedBy == operationID, nil
	}
	stored.UsedBy = operationID
	next, err := state.NewResource(CollectionApprovals, r.ID, 0, stored, r.Index, s.now())
	if err != nil {
		return false, err
	}
	_, err = s.Store.PutResource(ctx, next, r.Revision)
	if !errors.Is(err, state.ErrRevisionConflict) {
		return err == nil, err
	}
	latest, err := s.Store.Resource(ctx, CollectionApprovals, r.ID)
	if err != nil {
		return false, err
	}
	if err := latest.Decode(&stored); err != nil {
		return false, err
	}
	return stored.UsedBy == operationID, nil
}

func originSource(origin string) string {
	if origin == "user" || origin == "" {
		return "user_request"
	}
	return "automation"
}

var errRuleExhausted = errors.New("the permission rule's operation limit is used up")

func (s *Service) consume(ctx context.Context, rule Rule) error {
	if rule.MaxOperations == 0 {
		return nil
	}
	for attempt := 0; attempt < 8; attempt++ {
		r, err := s.Store.Resource(ctx, CollectionRules, rule.ID)
		if err != nil {
			return err
		}
		var stored storedRule
		if err = r.Decode(&stored); err != nil {
			return err
		}
		if stored.MaxOperations == 0 {
			return nil
		}
		if stored.Used >= stored.MaxOperations {
			return errRuleExhausted
		}
		stored.Used++
		next, err := state.NewResource(CollectionRules, rule.ID, 0, stored, r.Index, s.now())
		if err != nil {
			return err
		}
		if _, err = s.Store.PutResource(ctx, next, r.Revision); !errors.Is(err, state.ErrRevisionConflict) {
			return err
		}
	}
	return state.ErrRevisionConflict
}

type authority struct {
	RunID        string    `json:"run_id"`
	Revision     int64     `json:"revision"`
	Source       string    `json:"source"`
	RuleIDs      []string  `json:"rule_ids,omitempty"`
	Interaction  string    `json:"interaction_id,omitempty"`
	Digest       string    `json:"digest"`
	ConnectionID string    `json:"connection_id,omitempty"`
	Epoch        int64     `json:"epoch"`
	At           time.Time `json:"at"`
}

func (s *Service) allow(ctx context.Context, req Request, source string, rules []string, interaction string) (Decision, error) {
	now := s.now()
	evidence := authority{RunID: req.RunID, Revision: req.Revision, Source: source, RuleIDs: rules, Interaction: interaction,
		Digest: req.Digest, ConnectionID: req.ConnectionID, Epoch: req.Epoch, At: now}
	data, _ := json.Marshal([]any{req.RunID, req.Revision, req.Digest, source, rules, interaction, req.Epoch})
	sum := sha256.Sum256(data)
	ref := "auth_" + hex.EncodeToString(sum[:20])
	r, err := state.NewResource(CollectionAuthority, ref, 0, evidence, map[string]string{"task_id": req.RunID}, now)
	if err != nil {
		return Decision{}, err
	}
	if _, err = s.Store.PutResource(ctx, r, 0); err != nil && !errors.Is(err, state.ErrRevisionConflict) {
		return Decision{}, err
	}
	return Decision{Result: "allow", Source: source, AuthorityRef: ref, RuleIDs: rules}, nil
}

func (s *Service) AuthoritySource(ctx context.Context, ref string) string {
	if ref == "" {
		return ""
	}
	r, err := s.Store.Resource(ctx, CollectionAuthority, ref)
	if err != nil {
		return ""
	}
	var a authority
	if r.Decode(&a) != nil {
		return ""
	}
	return a.Source
}
