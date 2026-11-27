package state

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"
)

const CollectionRunTaint = "run_taint"

// RunTaint tracks whether a task has ingested untrusted external content
// (staged uploads, web pages, emails) and which domains are explicitly trusted
// (from user prompts or search results). Persisted in ResourceStore so it
// survives worker restarts and lease handoffs.
type RunTaint struct {
	Tainted        bool     `json:"tainted"`
	Sources        []string `json:"sources,omitempty"`
	TrustedDomains []string `json:"trusted_domains,omitempty"`
}

type TaintStore interface {
	Resource(ctx context.Context, collection, id string) (Resource, error)
	PutResource(ctx context.Context, r Resource, expected int64) (Resource, error)
}

func asTaintStore(store any) TaintStore {
	ts, _ := store.(TaintStore)
	return ts
}

// GetRunTaint returns the current taint record for runID.
// Fails closed: if the backing store returns an unexpected error (other than ErrNotFound)
// or corrupted JSON, returns Tainted=true so untrusted-content guards stay enforced.
func GetRunTaint(ctx context.Context, store any, runID string) RunTaint {
	ts := asTaintStore(store)
	if ts == nil || runID == "" {
		return RunTaint{}
	}
	res, err := ts.Resource(ctx, CollectionRunTaint, runID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return RunTaint{}
		}
		return RunTaint{Tainted: true, Sources: []string{"store_error_fail_closed"}}
	}
	var t RunTaint
	if err := res.Decode(&t); err != nil {
		return RunTaint{Tainted: true, Sources: []string{"decode_error_fail_closed"}}
	}
	return t
}

// MarkRunTainted durably marks runID as having ingested untrusted content from source.
// Merges monotonically with compare-and-swap retries so concurrent writes never drop Tainted=true.
func MarkRunTainted(ctx context.Context, store any, runID, source string) error {
	return updateRunTaint(ctx, store, runID, func(cur *RunTaint) {
		cur.Tainted = true
		if s := strings.TrimSpace(source); s != "" && !slices.Contains(cur.Sources, s) {
			if len(cur.Sources) < 64 {
				cur.Sources = append(cur.Sources, s)
			}
		}
	})
}

// AddRunTrustedDomains records trusted domains (for example from web_search results
// returned for a user query) so the browser/shell can follow them without unnecessary friction.
// Never clears Tainted.
func AddRunTrustedDomains(ctx context.Context, store any, runID string, rawDomains []string) error {
	var normalized []string
	for _, raw := range rawDomains {
		if d := NormalizeDomain(raw); d != "" && !slices.Contains(normalized, d) {
			normalized = append(normalized, d)
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	return updateRunTaint(ctx, store, runID, func(cur *RunTaint) {
		for _, d := range normalized {
			if !slices.Contains(cur.TrustedDomains, d) && len(cur.TrustedDomains) < 128 {
				cur.TrustedDomains = append(cur.TrustedDomains, d)
			}
		}
	})
}

func updateRunTaint(ctx context.Context, store any, runID string, mutate func(*RunTaint)) error {
	ts := asTaintStore(store)
	if ts == nil || runID == "" {
		return nil
	}
	const maxAttempts = 6
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var cur RunTaint
		var expected int64
		res, err := ts.Resource(ctx, CollectionRunTaint, runID)
		if err == nil {
			expected = res.Revision
			if dErr := res.Decode(&cur); dErr != nil {
				cur.Tainted = true
			}
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		wasTainted := cur.Tainted
		mutate(&cur)
		// Taint is strictly monotonic: once tainted, never untaint.
		if wasTainted {
			cur.Tainted = true
		}
		rec, err := NewResource(CollectionRunTaint, runID, expected+1, cur, map[string]string{"run_id": runID}, time.Now().UTC())
		if err != nil {
			return err
		}
		if _, err := ts.PutResource(ctx, rec, expected); err != nil {
			lastErr = err
			if errors.Is(err, ErrRevisionConflict) {
				continue
			}
			return err
		}
		return nil
	}
	return lastErr
}

// NormalizeDomain extracts a lowercased bare hostname from either a URL or host string.
func NormalizeDomain(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
			return strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
		}
	}
	s = strings.SplitN(s, "/", 2)[0]
	s = strings.SplitN(s, ":", 2)[0]
	s = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
	if s == "" || !strings.Contains(s, ".") {
		return ""
	}
	return s
}

// DomainMatches reports whether target matches trusted (exact or subdomain).
func DomainMatches(target, trusted string) bool {
	target = NormalizeDomain(target)
	trusted = NormalizeDomain(trusted)
	if target == "" || trusted == "" {
		return false
	}
	return target == trusted || strings.HasSuffix(target, "."+trusted)
}
