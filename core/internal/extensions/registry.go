package extensions

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type Source interface {
	Inventory(ctx context.Context) (Inventory, error)
}

type Inventory struct {
	Definitions  []Definition
	Capabilities []CapabilityState
}

type CapabilityState struct {
	Capability   string
	Title        string
	Account      string
	Availability string
	Ready        bool
}

type Resolver interface {
	Resolve(ctx context.Context, ref DefinitionRef) (Definition, error)
}

type Snapshot struct {
	order    []Definition
	byName   map[string]int
	byID     map[string]int
	Rejected []string
}

func NewSnapshot(definitions ...[]Definition) *Snapshot {
	s := &Snapshot{byName: map[string]int{}, byID: map[string]int{}}
	for _, group := range definitions {
		for _, d := range group {
			if _, taken := s.byName[d.ModelName]; taken {
				s.Rejected = append(s.Rejected, fmt.Sprintf("%s: model name %s is already in use", d.Ref.ID, d.ModelName))
				continue
			}
			if _, taken := s.byID[d.Ref.ID]; taken {
				s.Rejected = append(s.Rejected, fmt.Sprintf("%s: definition is listed twice", d.Ref.ID))
				continue
			}
			s.byName[d.ModelName] = len(s.order)
			s.byID[d.Ref.ID] = len(s.order)
			s.order = append(s.order, d)
		}
	}
	return s
}

func (s *Snapshot) Lookup(modelName string) (Definition, bool) {
	if s == nil {
		return Definition{}, false
	}
	i, ok := s.byName[modelName]
	if !ok {
		return Definition{}, false
	}
	return s.order[i], true
}

func (s *Snapshot) ByID(id string) (Definition, bool) {
	if s == nil {
		return Definition{}, false
	}
	i, ok := s.byID[id]
	if !ok {
		return Definition{}, false
	}
	return s.order[i], true
}

func (s *Snapshot) Definitions() []Definition {
	if s == nil {
		return nil
	}
	return append([]Definition{}, s.order...)
}

func (s *Snapshot) Search(query string, limit int) []Definition {
	words := strings.Fields(strings.ToLower(query))
	type hit struct {
		d     Definition
		score int
	}
	var hits []hit
	for _, d := range s.Definitions() {
		if d.Exposure == ExposureDisabled {
			continue
		}
		text := strings.ToLower(strings.Join([]string{d.ModelName, d.Title, d.Capability, d.CapabilityTitle, d.Description}, " "))
		score := 0
		for _, word := range words {
			if strings.Contains(text, word) {
				score++
			}
		}
		if score > 0 || len(words) == 0 {
			hits = append(hits, hit{d, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]Definition, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.d)
	}
	return out
}

type Sources []Source

func (s Sources) Inventory(ctx context.Context) (Inventory, error) {
	var merged Inventory
	var failure error
	for _, source := range s {
		inventory, err := source.Inventory(ctx)
		if err != nil {
			failure = err
			continue
		}
		merged.Definitions = append(merged.Definitions, inventory.Definitions...)
		merged.Capabilities = append(merged.Capabilities, inventory.Capabilities...)
	}
	if failure != nil && len(merged.Definitions) == 0 {
		return merged, failure
	}
	return merged, nil
}
