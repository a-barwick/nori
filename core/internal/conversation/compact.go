package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"slices"
	"strings"
	"time"

	"nori/internal/agent"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/transcript"
	"nori/internal/usage"
)

// compactionInstructions tell the summary model what to keep. The runtime checks every quote against the user's saved
// messages, so a paraphrase is replaced by their whole message or dropped.
var summaryTool = transcript.Tool{
	Name:        "save_summary",
	Description: prompts.Runtime("summary-save", nil),
	Parameters: &transcript.Schema{Type: transcript.TypeObject, Required: []string{"items", "gist"}, Properties: map[string]*transcript.Schema{
		"items": {Type: transcript.TypeArray, Items: &transcript.Schema{
			Type: transcript.TypeObject, Required: []string{"kind", "quote", "input_id", "note"},
			Properties: map[string]*transcript.Schema{
				"kind":     {Type: transcript.TypeString, Enum: state.SummaryKinds},
				"quote":    {Type: transcript.TypeString, Description: prompts.Runtime("summary-quote", nil)},
				"input_id": {Type: transcript.TypeString, Description: prompts.Runtime("summary-input", nil)},
				"note":     {Type: transcript.TypeString, Description: prompts.Runtime("summary-note", nil)},
			},
		}},
		"gist": {Type: transcript.TypeString},
	}},
}

// SummaryModel writes the conversation summary. The runtime uses the inner task model for it.
type SummaryModel interface {
	Name() string
	Respond(ctx context.Context, req agent.Request) (agent.Response, error)
}

const compactionPause = 10 * time.Minute

// Older records can carry an operator-set context cutoff. Respect it when replaying history.
func (r *Runner) contextStart(ctx context.Context) (time.Time, error) {
	control, _, err := r.tasks.Store.ConversationStatus(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return control.ContextFrom, nil
}

// compact folds the turns that no longer fit the recent window into the summary. It runs only while no user message
// is waiting, and stops as soon as one arrives.
func (r *Runner) compact(ctx context.Context) {
	if r.summarizer == nil || time.Now().Before(r.compactAfter) {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if pending, err := r.tasks.Store.UserRoutingPending(ctx); err == nil && pending {
					cancel()
					return
				}
			}
		}
	}()
	if err := r.compactOnce(ctx); err != nil && ctx.Err() == nil {
		log.Printf("conversation compaction failed: %v", err)
		r.compactAfter = time.Now().Add(compactionPause)
	}
}

func (r *Runner) compactOnce(ctx context.Context) error {
	from, err := r.contextStart(ctx)
	if err != nil {
		return err
	}
	summary, err := r.tasks.Store.ConversationSummary(ctx)
	if err != nil {
		return err
	}
	after := from
	if summary.Applies(from) {
		after = summary.Through
	} else {
		summary = state.ConversationSummary{From: from}
	}
	if summary.Items == nil {
		summary.Items = []state.SummaryItem{}
	}
	turns, err := r.tasks.Store.ConversationTurnsAfter(ctx, after)
	if err != nil {
		return err
	}
	// A full page may not reach back to after, so the oldest turns come from a second, oldest-first read.
	pageFull := len(turns) == state.HistoryTurnsExamined
	turns = slices.DeleteFunc(turns, func(t state.ConversationTurn) bool { return t.Status != "completed" })
	size := 0
	for _, t := range turns {
		size += state.TurnBytes(t)
	}
	if len(turns) <= state.RecentTurnLimit && size <= state.RecentTurnBytes {
		return nil
	}
	// Newest first: keep the newest turns out, then summarize the rest oldest first, within the read bounds. The
	// summary's through never moves past a turn compaction didn't read.
	older := turns[min(state.CompactKeep, len(turns)):]
	if pageFull && len(older) > 0 {
		newest := older[0].StartedAt
		if older, err = r.tasks.Store.OldestConversationTurnsAfter(ctx, after); err != nil {
			return err
		}
		older = slices.DeleteFunc(older, func(t state.ConversationTurn) bool {
			return t.Status != "completed" || t.StartedAt.After(newest)
		})
	} else {
		slices.Reverse(older)
	}
	if len(older) == 0 {
		return nil
	}
	sources := map[string]string{}
	for _, item := range summary.Items {
		sources[item.InputID] = strings.TrimPrefix(sources[item.InputID]+"\n"+item.Quote, "\n")
	}
	rendered, through, count := []any{}, time.Time{}, 0
	inputBytes := 0
	for _, t := range older {
		view, users := compactionTurn(t)
		data, _ := json.Marshal(view)
		if count > 0 && (count >= state.CompactTurnLimit || inputBytes+len(data) > state.CompactInputBytes) {
			break
		}
		inputBytes += len(data)
		rendered = append(rendered, view)
		for id, text := range users {
			sources[id] = text
		}
		through, count = t.StartedAt, count+1
	}
	prompt, _ := json.Marshal(map[string]any{
		"previous_summary": map[string]any{"items": summary.Items, "gist": summary.Gist}, "turns": rendered,
	})
	items, gist, err := r.summarize(ctx, string(prompt))
	if err != nil {
		return err
	}
	kept, dropped := state.CheckSummaryItems(items, sources)
	if dropped > 0 {
		log.Printf("conversation compaction dropped %d summary items whose quotes weren't the user's words", dropped)
	}
	next := state.ConversationSummary{
		From: from, Through: through, Items: kept, Gist: strings.TrimSpace(gist), Turns: summary.Turns + count,
		Model: r.summarizer.Name(), UpdatedAt: time.Now().UTC(),
	}
	state.FitSummary(&next)
	log.Printf("conversation compaction summarized %d turns into %d items", count, len(next.Items))
	return r.tasks.Store.SaveConversationSummary(ctx, next)
}

func (r *Runner) summarize(ctx context.Context, prompt string) ([]state.SummaryItem, string, error) {
	response, err := r.summarizer.Respond(usage.WithScope(ctx, "compaction", "compaction"), agent.Request{
		Instructions: prompts.Compaction, Tools: []transcript.Tool{summaryTool}, MaxOutputTokens: 4096, Reasoning: "medium",
		Messages: []transcript.Message{{Role: transcript.RoleUser, Parts: []transcript.Part{{Text: prompt}}}},
	})
	if err != nil {
		return nil, "", err
	}
	if len(response.Calls) != 1 || response.Calls[0].Name != summaryTool.Name {
		return nil, "", errors.New("summary model didn't call save_summary")
	}
	data, _ := json.Marshal(response.Calls[0].Args)
	var args struct {
		Items []state.SummaryItem `json:"items"`
		Gist  string              `json:"gist"`
	}
	if err := json.Unmarshal(data, &args); err != nil {
		return nil, "", errors.New("summary model returned an invalid summary")
	}
	return args.Items, args.Gist, nil
}

func compactionTurn(t state.ConversationTurn) (map[string]any, map[string]string) {
	users := map[string]string{}
	messages := []map[string]string{}
	for _, in := range t.Inputs {
		switch {
		case in.IsUser():
			users[in.ID] = in.Text
			messages = append(messages, map[string]string{"from": "user", "input_id": in.ID, "text": in.Text})
		case in.Event != nil:
			text := in.Event.Text
			if len(text) > 600 {
				text = text[:600]
			}
			messages = append(messages, map[string]string{"from": "task " + in.Event.RunID, "event": in.Event.Kind, "text": strings.ToValidUTF8(text, "")})
		}
	}
	if reply := publishedReply(t); reply != "" {
		messages = append(messages, map[string]string{"from": "Nori", "text": reply})
	}
	return map[string]any{"started_at": t.StartedAt, "messages": messages}, users
}

func publishedReply(t state.ConversationTurn) string {
	for i := len(t.Protocol) - 1; i >= 0; i-- {
		var m struct {
			Role    string  `json:"role"`
			Content *string `json:"content"`
		}
		if json.Unmarshal([]byte(t.Protocol[i]), &m) != nil || m.Role != "assistant" || m.Content == nil {
			continue
		}
		var structured struct {
			Text *string `json:"text"`
		}
		if json.Unmarshal([]byte(*m.Content), &structured) == nil && structured.Text != nil {
			return *structured.Text
		}
		return *m.Content
	}
	return ""
}
