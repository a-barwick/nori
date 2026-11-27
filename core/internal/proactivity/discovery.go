package proactivity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"nori/internal/state"
)

const (
	// CollectionProactivityDiscovery stores assistant-mutated organic discovery
	// state in its own resource collection so assistant writes never race with
	// user Preferences saves.
	CollectionProactivityDiscovery = "proactivity_discovery"
	DiscoveryResourceID            = "default"

	FeaturePreMeetingPrep = "pre_meeting_prep"
	FeatureEODWrap        = "eod_wrap"
	FeatureSourceWatch    = "source_watch"
)

// ErrUnknownFeature is returned when a caller references an unsupported
// proactivity discovery feature name.
var ErrUnknownFeature = errors.New("unknown proactivity discovery feature")

// ResourceStore is the narrow persistence interface needed for discovery state.
// state.ResourceStore satisfies this interface directly.
type ResourceStore interface {
	Resource(ctx context.Context, collection, id string) (state.Resource, error)
	PutResource(ctx context.Context, r state.Resource, expected int64) (state.Resource, error)
}

// FeatureDiscovery tracks organic discovery offer state for a single feature.
type FeatureDiscovery struct {
	OfferCount    int        `json:"offer_count"`
	LastOfferedAt *time.Time `json:"last_offered_at,omitempty"`
	Declined      bool       `json:"declined,omitempty"`
	Accepted      bool       `json:"accepted,omitempty"`
}

// DiscoveryState holds assistant-mutated discovery state for Nori's organic
// proactivity features. It is persisted in CollectionProactivityDiscovery
// independently of user Preferences.
type DiscoveryState struct {
	Revision       int64            `json:"revision"`
	PreMeetingPrep FeatureDiscovery `json:"pre_meeting_prep"`
	EODWrap        FeatureDiscovery `json:"eod_wrap"`
	SourceWatch    FeatureDiscovery `json:"source_watch"`
	UpdatedAt      *time.Time       `json:"updated_at,omitempty"`
}

// Feature returns a pointer to the named FeatureDiscovery slot.
func (d *DiscoveryState) Feature(name string) (*FeatureDiscovery, bool) {
	if d == nil {
		return nil, false
	}
	switch normalizeFeature(name) {
	case FeaturePreMeetingPrep:
		return &d.PreMeetingPrep, true
	case FeatureEODWrap:
		return &d.EODWrap, true
	case FeatureSourceWatch:
		return &d.SourceWatch, true
	default:
		return nil, false
	}
}

// LastOfferedAt returns the most recent offer timestamp across all features.
func (d DiscoveryState) LastOfferedAt() time.Time {
	var latest time.Time
	for _, f := range []FeatureDiscovery{d.PreMeetingPrep, d.EODWrap, d.SourceWatch} {
		if f.LastOfferedAt != nil && f.LastOfferedAt.After(latest) {
			latest = *f.LastOfferedAt
		}
	}
	return latest
}

func normalizeFeature(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// DiscoveryService manages assistant-mutated discovery state with optimistic
// compare-and-swap retries against CollectionProactivityDiscovery.
type DiscoveryService struct {
	Store ResourceStore
	Now   func() time.Time
}

func (s *DiscoveryService) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Get returns the current discovery state, or a zero-value DiscoveryState if
// none has been stored yet.
func (s *DiscoveryService) Get(ctx context.Context) (DiscoveryState, error) {
	if s == nil || s.Store == nil {
		return DiscoveryState{}, nil
	}
	res, err := s.Store.Resource(ctx, CollectionProactivityDiscovery, DiscoveryResourceID)
	if errors.Is(err, state.ErrNotFound) {
		return DiscoveryState{}, nil
	}
	if err != nil {
		return DiscoveryState{}, err
	}
	var ds DiscoveryState
	if err := res.Decode(&ds); err != nil {
		return DiscoveryState{}, err
	}
	ds.Revision = res.Revision
	if !res.UpdatedAt.IsZero() {
		u := res.UpdatedAt.UTC()
		ds.UpdatedAt = &u
	}
	return ds, nil
}

// RecordOffer increments offer_count and updates last_offered_at for feature.
func (s *DiscoveryService) RecordOffer(ctx context.Context, feature string) (DiscoveryState, error) {
	return s.update(ctx, feature, func(f *FeatureDiscovery, now time.Time) {
		f.OfferCount++
		ts := now
		f.LastOfferedAt = &ts
	})
}

// RecordDecision records whether the user accepted or declined a discovery offer.
func (s *DiscoveryService) RecordDecision(ctx context.Context, feature string, accepted bool) (DiscoveryState, error) {
	return s.update(ctx, feature, func(f *FeatureDiscovery, _ time.Time) {
		if accepted {
			f.Accepted = true
			f.Declined = false
		} else {
			f.Declined = true
			f.Accepted = false
		}
	})
}

func (s *DiscoveryService) update(ctx context.Context, feature string, mutate func(f *FeatureDiscovery, now time.Time)) (DiscoveryState, error) {
	canonical := normalizeFeature(feature)
	var probe DiscoveryState
	if _, ok := probe.Feature(canonical); !ok {
		return DiscoveryState{}, fmt.Errorf("%w: %q", ErrUnknownFeature, feature)
	}
	if s == nil || s.Store == nil {
		return DiscoveryState{}, errors.New("proactivity discovery store is not configured")
	}

	const maxAttempts = 6
	var lastErr error
	for range maxAttempts {
		var cur DiscoveryState
		var expected int64
		res, err := s.Store.Resource(ctx, CollectionProactivityDiscovery, DiscoveryResourceID)
		if err == nil {
			expected = res.Revision
			if err := res.Decode(&cur); err != nil {
				return DiscoveryState{}, err
			}
		} else if !errors.Is(err, state.ErrNotFound) {
			return DiscoveryState{}, err
		}

		now := s.now()
		slot, _ := cur.Feature(canonical)
		mutate(slot, now)
		cur.Revision = expected + 1
		cur.UpdatedAt = &now

		rec, err := state.NewResource(
			CollectionProactivityDiscovery,
			DiscoveryResourceID,
			cur.Revision,
			cur,
			nil,
			now,
		)
		if err != nil {
			return DiscoveryState{}, err
		}
		saved, err := s.Store.PutResource(ctx, rec, expected)
		if err != nil {
			lastErr = err
			if errors.Is(err, state.ErrRevisionConflict) {
				continue
			}
			return DiscoveryState{}, err
		}
		cur.Revision = saved.Revision
		return cur, nil
	}
	return DiscoveryState{}, lastErr
}
