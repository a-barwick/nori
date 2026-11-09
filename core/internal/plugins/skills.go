package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"nori/internal/state"
)

type skillDoc struct {
	Skill          Skill  `json:"skill"`
	InstallationID string `json:"installation_id,omitempty"`
	Disabled       bool   `json:"disabled,omitempty"`
}

// SkillView is a skill with its resource revision for editing.
type SkillView struct {
	Skill
	ID             string `json:"id"`
	Revision       int64  `json:"revision"`
	InstallationID string `json:"installation_id,omitempty"`
	Enabled        bool   `json:"enabled"`
}

func skillKey(ref string) string {
	return "skl_" + strings.NewReplacer("/", "__", ".", "_").Replace(ref)
}

func (s *Service) putSkill(ctx context.Context, skill Skill, installationID string) (state.Resource, error) {
	if skill.Inputs == nil {
		skill.Inputs = map[string]any{"type": "object"}
	}
	if skill.RequiredToolIDs == nil {
		skill.RequiredToolIDs = []string{}
	}
	key := skillKey(skill.Ref.ID)
	r, err := state.NewResource(CollectionSkills, key, 0, skillDoc{Skill: skill, InstallationID: installationID}, map[string]string{"installation_id": installationID}, s.now())
	if err != nil {
		return r, err
	}
	current, err := s.Store.Resource(ctx, CollectionSkills, key)
	expected := int64(0)
	if err == nil {
		expected = current.Revision
	}
	return s.Store.PutResource(ctx, r, expected)
}

func view(r state.Resource) (SkillView, error) {
	var doc skillDoc
	if err := r.Decode(&doc); err != nil {
		return SkillView{}, err
	}
	return SkillView{Skill: doc.Skill, ID: r.ID, Revision: r.Revision, InstallationID: doc.InstallationID, Enabled: !doc.Disabled}, nil
}

func (s *Service) Skills(ctx context.Context) ([]SkillView, error) {
	page, _, err := s.Store.Resources(ctx, CollectionSkills, nil, "", 200)
	if err != nil {
		return nil, err
	}
	out := []SkillView{}
	for _, r := range page {
		if v, err := view(r); err == nil {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *Service) Skill(ctx context.Context, id string) (SkillView, error) {
	r, err := s.Store.Resource(ctx, CollectionSkills, id)
	if err != nil {
		return SkillView{}, err
	}
	return view(r)
}

// SkillEditable is the contract SkillEditable.
type SkillEditable struct {
	Title           string         `json:"title"`
	Description     string         `json:"description"`
	Instructions    string         `json:"instructions"`
	Inputs          map[string]any `json:"inputs"`
	RequiredToolIDs []string       `json:"required_tool_ids"`
	Examples        []string       `json:"examples,omitempty"`
	Scope           string         `json:"scope"`
}

// SaveSkill creates or edits a user skill. Every edit is a new version, so a run records exactly what it used.
// Packaged skills are read-only; edit a copy instead.
func (s *Service) SaveSkill(ctx context.Context, id, requestID string, expected int64, value SkillEditable) (state.MutationReceipt, error) {
	if strings.TrimSpace(value.Title) == "" || strings.TrimSpace(value.Instructions) == "" || len(value.Instructions) > 32<<10 {
		return state.MutationReceipt{}, fmt.Errorf("%w: a skill needs a title and instructions under 32 KiB", state.ErrInvalidResource)
	}
	method, target, status := "PUT", "/v1/skills/"+id, 200
	if id == "" {
		id = "skl_user__" + strings.ReplaceAll(uuid.NewSHA1(uuid.NameSpaceOID, []byte("skill:"+requestID)).String(), "-", "")[:16]
		method, target, status = "POST", "/v1/skills", 201
	}
	return s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: method, Target: target, ExpectedRevision: expected, Payload: value, Collection: CollectionSkills, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			revision := int64(1)
			if current != nil {
				var doc skillDoc
				if err := current.Decode(&doc); err != nil {
					return state.MutationResult{}, err
				}
				if doc.InstallationID != "" {
					return state.MutationResult{}, fmt.Errorf("%w: a packaged skill cannot be edited", state.ErrInvalidResource)
				}
				revision = current.Revision + 1
			}
			skill := Skill{Ref: Ref{ID: "user/" + strings.TrimPrefix(id, "skl_user__"), Version: strconv.FormatInt(revision, 10)},
				Title: value.Title, Description: value.Description, Instructions: value.Instructions, Inputs: value.Inputs,
				RequiredToolIDs: nonNil(value.RequiredToolIDs), Examples: value.Examples, Scope: value.Scope}
			if skill.Inputs == nil {
				skill.Inputs = map[string]any{"type": "object"}
			}
			r, err := state.NewResource(CollectionSkills, id, revision, skillDoc{Skill: skill}, map[string]string{"installation_id": ""}, s.now())
			if err != nil {
				return state.MutationResult{}, err
			}
			data, _ := json.Marshal(SkillView{Skill: skill, ID: id, Revision: revision, Enabled: true})
			return state.MutationResult{Writes: []state.Resource{r}, Receipt: state.MutationReceipt{Status: "applied", Resource: data, HTTPStatus: status}}, nil
		},
	})
}

// DeleteSkill removes a user skill. Runs that used it keep the version they recorded.
func (s *Service) DeleteSkill(ctx context.Context, id, requestID string, expected int64) (state.MutationReceipt, error) {
	return s.Store.Mutate(ctx, state.Mutation{
		RequestID: requestID, Method: "DELETE", Target: "/v1/skills/" + id, ExpectedRevision: expected, Payload: id, Collection: CollectionSkills, ID: id,
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			if current == nil {
				return state.MutationResult{}, state.ErrNotFound
			}
			return state.MutationResult{Deletes: []state.ResourceKey{{Collection: CollectionSkills, ID: id}},
				Receipt: state.MutationReceipt{Status: "applied", Resource: current.Body, HTTPStatus: 200}}, nil
		},
	})
}
