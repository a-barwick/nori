package extensions

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Source supplies the definitions a user can currently choose from beyond the engine's own: native App connectors
// for their Connections, discovered MCP tools and plugin contributions. It runs in activities, never in workflow
// code.
type Source interface {
	Inventory(ctx context.Context) (Inventory, error)
}

// Inventory is what a Source currently offers: definitions plus the account and availability text the capability
// catalog shows. It never contains credentials.
type Inventory struct {
	Definitions  []Definition
	Capabilities []CapabilityState
}

// CapabilityState describes one capability for the model's catalog and capability explanations, from metadata only.
type CapabilityState struct {
	Capability   string
	Title        string
	Account      string
	Availability string
	Ready        bool
}

// Resolver finds the exact definition a pinned call names, even after a newer version replaced it for selection.
type Resolver interface {
	Resolve(ctx context.Context, ref DefinitionRef) (Definition, error)
}

// Snapshot is an immutable, ordered set of definitions with unique model names. Order is stable so model requests
// keep their prompt-cache prefix.
type Snapshot struct {
	order  []Definition
	byName map[string]int
	byID   map[string]int
	// Rejected lists definitions dropped because their model name was taken, with the reason.
	Rejected []string
}

// NewSnapshot builds a snapshot. Earlier definitions win a model-name collision, so pass built-ins first.
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

// Search ranks enabled definitions by how many query words their name, title, capability and description contain.
// It reads metadata only.
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

// Sources merges several sources in order. A failing source is reported, and the others still contribute.
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
