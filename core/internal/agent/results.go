package agent

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"nori/internal/prompts"
	"nori/internal/state"
)

var resultNote = prompts.Runtime("result-source-note", nil)

const (
	resultSearchLimit = 300
	resultSearchRuns  = 100
	resultFound       = 10
)

func validTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" || len(title) > state.MaxResultTitle {
		return "", errors.New(prompts.Runtime("tool-a-saved-result-needs-a-title-of-1-200-bytes", nil))
	}
	return title, nil
}

func (s *Service) resultLink(ctx context.Context, run, address, title string) (map[string]any, error) {
	title, err := validTitle(title)
	if err != nil {
		return nil, err
	}
	if err = state.ValidLink(address); err != nil {
		return nil, err
	}
	record, err := s.Store.GetRun(ctx, run)
	if err != nil {
		return nil, err
	}
	id := state.ResultID(run, address)
	saved, err := s.Store.SaveResult(ctx, state.Result{
		ID: id, RootID: id, Version: 1, RunID: run, Revision: record.Revision, Kind: state.ResultLink, Title: title, URL: address, CreatedAt: s.currentTime().UTC(),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"result_id": saved.ID, "artifact_id": saved.ID, "title": saved.Title, "url": saved.URL}, nil
}

// resultsFind searches saved results, and tasks from before results existed. There are few enough for one owner to read
// the newest and match words, and an index would have to be kept in step with them.
func (s *Service) resultsFind(ctx context.Context, query string) (map[string]any, error) {
	words := []string{}
	for _, word := range strings.Fields(strings.ToLower(query)) {
		if len(word) >= 3 {
			words = append(words, word)
		}
	}
	results, err := s.Store.RecentResults(ctx, resultSearchLimit)
	if err != nil {
		return nil, err
	}
	if s.Hidden != nil {
		visible := results[:0]
		for _, result := range results {
			if !s.Hidden(ctx, result.ID) {
				visible = append(visible, result)
			}
		}
		results = visible
	}
	runs, err := s.Store.RecentRuns(ctx, resultSearchRuns)
	if err != nil {
		return nil, err
	}
	type match struct {
		score int
		at    time.Time
		item  map[string]any
	}
	score := func(texts ...string) int {
		joined := strings.ToLower(strings.Join(texts, " "))
		count := 0
		for _, word := range words {
			if strings.Contains(joined, word) {
				count++
			}
		}
		return count
	}
	matches := []match{}
	covered := map[string]bool{}
	for _, result := range results {
		covered[result.RunID] = true
		if n := score(result.Title, result.Text, result.Name, result.URL); n > 0 {
			matches = append(matches, match{n, result.CreatedAt, map[string]any{
				"result_id": result.ID, "task_id": result.RunID, "title": result.Title, "kind": result.Kind, "version": result.Version,
				"superseded_by": result.SupersededBy, "created_at": result.CreatedAt, "snippet": snippet(result.Text),
			}})
		}
	}
	for _, run := range runs {
		if covered[run.ID] {
			continue
		}
		if n := score(run.Request, run.Result, run.Error); n > 0 {
			matches = append(matches, match{n, run.StartedAt, map[string]any{
				"task_id": run.ID, "title": state.ResultTitle(run.Request), "status": run.Status, "created_at": run.StartedAt,
				"snippet": snippet(run.Result + " " + run.Error),
			}})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return matches[i].at.After(matches[j].at)
	})
	found := []map[string]any{}
	for i := 0; i < len(matches) && i < resultFound; i++ {
		found = append(found, matches[i].item)
	}
	return map[string]any{"results": found, "note": resultNote}, nil
}

func snippet(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= 400 {
		return text
	}
	cut := 400
	for cut > 0 && text[cut]&0xC0 == 0x80 {
		cut--
	}
	return text[:cut] + "…"
}

func (s *Service) resultRead(ctx context.Context, id string) (map[string]any, error) {
	result, err := s.Store.GetResult(ctx, id)
	if err == nil && s.Hidden != nil && s.Hidden(ctx, id) {
		err = state.ErrNotFound
	}
	if err != nil {
		return nil, errors.New(prompts.Runtime("tool-no-saved-result-has-that-result-id", nil))
	}
	return map[string]any{
		"result_id": result.ID, "task_id": result.RunID, "kind": result.Kind, "title": result.Title, "text": result.Text, "name": result.Name,
		"url": result.URL, "version": result.Version, "revises": result.Revises, "superseded_by": result.SupersededBy, "unmet": result.Unmet, "note": resultNote,
	}, nil
}
