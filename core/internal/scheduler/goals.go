package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"nori/internal/state"
)

func decodeGoal(r state.Resource) (Goal, error) {
	var g Goal
	if err := r.Decode(&g); err != nil {
		return g, err
	}
	g.Revision = r.Revision
	created, updated := r.CreatedAt, r.UpdatedAt
	g.CreatedAt, g.UpdatedAt = &created, &updated
	return g, nil
}

func (s *Service) Goal(ctx context.Context, id string) (Goal, error) {
	r, err := s.Store.Resource(ctx, CollectionGoals, id)
	if err != nil {
		return Goal{}, err
	}
	g, err := decodeGoal(r)
	if err != nil {
		return Goal{}, err
	}
	return s.link(ctx, g), nil
}

func (s *Service) link(ctx context.Context, g Goal) Goal {
	if g.Linked.TaskIDs == nil {
		g.Linked.TaskIDs = []string{}
	}
	if g.Linked.ResultIDs == nil {
		g.Linked.ResultIDs = []string{}
	}
	g.Linked.AutomationIDs = []string{}
	if resources, err := s.allResources(ctx, CollectionAutomations, map[string]string{"goal_id": g.ID}, 100); err == nil {
		for _, r := range resources {
			g.Linked.AutomationIDs = append(g.Linked.AutomationIDs, r.ID)
		}
	}
	if s.Tasks != nil {
		if runs, err := s.Tasks.RecentRuns(ctx, 200); err == nil {
			for _, run := range runs {
				if run.Origin != nil && run.Origin.GoalID == g.ID && !slices.Contains(g.Linked.TaskIDs, run.ID) {
					g.Linked.TaskIDs = append(g.Linked.TaskIDs, run.ID)
				}
			}
		}
	}
	if s.Results != nil {
		for _, task := range g.Linked.TaskIDs {
			for _, result := range s.Results(ctx, task) {
				if !slices.Contains(g.Linked.ResultIDs, result) {
					g.Linked.ResultIDs = append(g.Linked.ResultIDs, result)
				}
			}
		}
	}
	return g
}

func (s *Service) Goals(ctx context.Context, status string) ([]Goal, error) {
	resources, err := s.allResources(ctx, CollectionGoals, map[string]string{"status": status}, 200)
	if err != nil {
		return nil, err
	}
	out := make([]Goal, 0, len(resources))
	for _, r := range resources {
		g, err := decodeGoal(r)
		if err != nil {
			return nil, err
		}
		out = append(out, s.link(ctx, g))
	}
	return out, nil
}

func criteria(value GoalEditable, previous []Criterion) ([]Criterion, error) {
	out := []Criterion{}
	for i, c := range value.CompletionCriteria {
		if strings.TrimSpace(c.Text) == "" {
			return nil, fmt.Errorf("%w: a completion criterion needs text", state.ErrInvalidResource)
		}
		id := c.ID
		if id == "" {
			id = fmt.Sprintf("c%d", i+1)
		}
		criterion := Criterion{ID: id, Text: c.Text}
		for _, p := range previous {
			if p.ID == id && p.Text == c.Text {
				criterion.Met, criterion.EvidenceResultIDs = p.Met, p.EvidenceResultIDs
			}
		}
		out = append(out, criterion)
	}
	return out, nil
}

func (s *Service) SaveGoal(ctx context.Context, id, requestID string, expected int64, value GoalEditable) (state.MutationReceipt, error) {
	if strings.TrimSpace(value.Title) == "" || strings.TrimSpace(value.Outcome) == "" {
		return state.MutationReceipt{}, fmt.Errorf("%w: a goal needs a title and an outcome", state.ErrInvalidResource)
	}
	switch value.FollowUp {
	case "", "none", "remind", "monitor":
	default:
		return state.MutationReceipt{}, state.ErrInvalidResource
	}
	method, target, status := "PUT", "/v1/goals/"+id, 200
	if id == "" {
		id = "goal_" + strings.ReplaceAll(uuid.NewSHA1(uuid.NameSpaceOID, []byte("goal:"+requestID)).String(), "-", "")[:20]
		method, target, status = "POST", "/v1/goals", 201
	}
	return s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: method, Target: target, ExpectedRevision: expected, Payload: value, Collection: CollectionGoals, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			g := Goal{ID: id, Status: "active", NextActions: []string{}, Blockers: []Blocker{}, Linked: GoalLinks{TaskIDs: []string{}, AutomationIDs: []string{}, ResultIDs: []string{}}}
			revision := int64(1)
			if current != nil {
				var err error
				if g, err = decodeGoal(*current); err != nil {
					return state.MutationResult{}, err
				}
				revision = current.Revision + 1
			}
			list, err := criteria(value, g.CompletionCriteria)
			if err != nil {
				return state.MutationResult{}, err
			}
			g.Title, g.Outcome, g.CompletionCriteria, g.Deadline, g.FollowUp = value.Title, value.Outcome, list, value.Deadline, value.FollowUp
			return s.goalWrite(g, revision, status)
		},
	})
}

func (s *Service) goalWrite(g Goal, revision int64, status int) (state.MutationResult, error) {
	g.Revision = revision
	resource, err := state.NewResource(CollectionGoals, g.ID, revision, g, map[string]string{"status": g.Status}, s.now())
	if err != nil {
		return state.MutationResult{}, err
	}
	data, _ := json.Marshal(g)
	return state.MutationResult{Writes: []state.Resource{resource}, Receipt: state.MutationReceipt{Status: "applied", Resource: data, HTTPStatus: status}}, nil
}

func (s *Service) ActGoal(ctx context.Context, id, requestID string, expected int64, action GoalAction) (state.MutationReceipt, error) {
	now := s.now()
	stopMonitors := false
	receipt, err := s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "POST", Target: "/v1/goals/" + id + "/actions", ExpectedRevision: expected, Payload: action,
		Collection: CollectionGoals, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			g, err := decodeGoal(*current)
			if err != nil {
				return state.MutationResult{}, err
			}
			switch {
			case action.Action == "pause" && g.Status == "active":
				g.Status = "paused"
			case action.Action == "resume" && g.Status == "paused":
				g.Status = "active"
			case action.Action == "complete" && (g.Status == "active" || g.Status == "paused"):
				g.Status, g.CompletedAt = "completed", &now
				stopMonitors = !action.KeepMonitors
			case action.Action == "abandon" && g.Status != "completed" && g.Status != "abandoned":
				g.Status = "abandoned"
				stopMonitors = !action.KeepMonitors
			default:
				return state.MutationResult{}, fmt.Errorf("%w: %s does not apply to a %s goal", state.ErrInvalidResource, action.Action, g.Status)
			}
			return s.goalWrite(g, current.Revision+1, 200)
		},
	})
	if err != nil || !stopMonitors {
		return receipt, err
	}
	resources, err := s.allResources(ctx, CollectionAutomations, map[string]string{"goal_id": id}, 100)
	if err != nil {
		return receipt, nil
	}
	for _, r := range resources {
		a, err := decodeAutomation(r)
		if err != nil || a.Status == AutomationCancelled {
			continue
		}
		cancelID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("goal-stop:"+requestID+":"+a.ID)).String()
		_, _ = s.Act(ctx, a.ID, cancelID, r.Revision, Action{Action: "cancel"})
	}
	return receipt, nil
}

type GoalProgress struct {
	Progress    string    `json:"progress,omitempty"`
	NextActions []string  `json:"next_actions,omitempty"`
	Blockers    []Blocker `json:"blockers,omitempty"`
	MetCriteria []struct {
		ID        string   `json:"id"`
		ResultIDs []string `json:"result_ids,omitempty"`
	} `json:"met_criteria,omitempty"`
	TaskID string `json:"-"`
}

func (s *Service) RecordProgress(ctx context.Context, id, requestID string, update GoalProgress) (Goal, error) {
	receipt, err := s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "PROGRESS", Target: id, Payload: update, Collection: CollectionGoals, ID: id, SkipRevisionCheck: true,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			g, err := decodeGoal(*current)
			if err != nil {
				return state.MutationResult{}, err
			}
			if g.Status != "active" {
				return state.MutationResult{}, fmt.Errorf("%w: the goal is %s", state.ErrInvalidResource, g.Status)
			}
			if update.Progress != "" {
				g.Progress = clip(update.Progress, 2000)
			}
			if update.NextActions != nil {
				g.NextActions = update.NextActions
			}
			if update.Blockers != nil {
				g.Blockers = update.Blockers
			}
			for _, met := range update.MetCriteria {
				for i := range g.CompletionCriteria {
					if g.CompletionCriteria[i].ID == met.ID {
						if len(met.ResultIDs) == 0 {
							return state.MutationResult{}, fmt.Errorf("%w: a met criterion needs cited results", state.ErrInvalidResource)
						}
						g.CompletionCriteria[i].Met, g.CompletionCriteria[i].EvidenceResultIDs = true, met.ResultIDs
					}
				}
			}
			if update.TaskID != "" && !slices.Contains(g.Linked.TaskIDs, update.TaskID) {
				g.Linked.TaskIDs = append(g.Linked.TaskIDs, update.TaskID)
			}
			return s.goalWrite(g, current.Revision+1, 200)
		},
	})
	if err != nil {
		return Goal{}, err
	}
	var g Goal
	err = json.Unmarshal(receipt.Resource, &g)
	return g, err
}
