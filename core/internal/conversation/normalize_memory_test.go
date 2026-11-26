package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"nori/internal/state"
)

func TestNormalizeManualMemorySuccessAndDedup(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	existing := []state.Memory{
		{
			ID:         "mem_existing_seat",
			Text:       "The user prefers aisle seats on flights.",
			About:      "the user",
			Section:    "Preferences",
			Provenance: state.ProvenanceUserExplicit,
			CreatedAt:  now,
			UpdatedAt:  now,
		},
		{
			ID:         "mem_task_outcome",
			Text:       "Checked flight status.",
			About:      "the user",
			Section:    "Context",
			Provenance: state.ProvenanceTaskOutcome,
			CreatedAt:  now,
			UpdatedAt:  now,
		},
	}

	model := &scriptedReplyModel{}
	model.SetHandler(func(_ context.Context, req ReplyRequest) (Reply, error) {
		if !req.NoDispatch {
			t.Fatalf("expected NoDispatch=true for normalizer call")
		}
		if len(req.Messages) != 2 {
			t.Fatalf("expected 2 messages, got %d", len(req.Messages))
		}
		if strings.Contains(string(req.Messages[1]), "mem_task_outcome") {
			t.Fatalf("task_outcome memory must not be included in normalizer input")
		}
		resp, _ := json.Marshal(map[string]any{
			"text":     "The user prefers window seats on flights.",
			"about":    "the user",
			"section":  "Preferences",
			"replaces": "mem_existing_seat",
		})
		return Reply{Text: string(resp)}, nil
	})

	out, err := NormalizeManualMemory(context.Background(), model, ManualMemoryInput{
		Text:     "actually I prefer window seats on flights",
		Existing: existing,
	})
	if err != nil {
		t.Fatalf("NormalizeManualMemory: %v", err)
	}
	if out.Text != "The user prefers window seats on flights." {
		t.Fatalf("Text = %q", out.Text)
	}
	if out.About != "the user" {
		t.Fatalf("About = %q", out.About)
	}
	if out.Section != "Preferences" {
		t.Fatalf("Section = %q", out.Section)
	}
	if out.ReplacesID != "mem_existing_seat" {
		t.Fatalf("ReplacesID = %q, want mem_existing_seat", out.ReplacesID)
	}
}

func TestNormalizeManualMemoryRespectsExplicitOverridesAndIgnoresUnknownReplaces(t *testing.T) {
	model := &scriptedReplyModel{}
	model.SetHandler(func(_ context.Context, _ ReplyRequest) (Reply, error) {
		return Reply{
			Text: "```json\n{\"text\":\"Sarah leads the mobile team.\",\"about\":\"someone else\",\"section\":\"Context\",\"replaces\":\"nonexistent_id\"}\n```",
		}, nil
	})

	out, err := NormalizeManualMemory(context.Background(), model, ManualMemoryInput{
		Text:    "Sarah leads mobile",
		About:   "Sarah",
		Section: "People",
	})
	if err != nil {
		t.Fatalf("NormalizeManualMemory: %v", err)
	}
	if out.Text != "Sarah leads the mobile team." {
		t.Fatalf("Text = %q", out.Text)
	}
	if out.About != "Sarah" {
		t.Fatalf("expected explicit About to be preserved, got %q", out.About)
	}
	if out.Section != "People" {
		t.Fatalf("expected explicit Section to be preserved, got %q", out.Section)
	}
	if out.ReplacesID != "" {
		t.Fatalf("expected unknown replaces ID to be ignored, got %q", out.ReplacesID)
	}
}

func TestNormalizeManualMemoryFallbacks(t *testing.T) {
	t.Run("nil model", func(t *testing.T) {
		out, err := NormalizeManualMemory(context.Background(), nil, ManualMemoryInput{
			Text: "  I drink oat milk lattes  ",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out.Text != "I drink oat milk lattes" || out.About != "the user" || out.Section != "" || out.ReplacesID != "" {
			t.Fatalf("unexpected fallback: %+v", out)
		}
	})

	t.Run("model error", func(t *testing.T) {
		model := &scriptedReplyModel{}
		model.SetHandler(func(context.Context, ReplyRequest) (Reply, error) {
			return Reply{}, errors.New("provider timeout")
		})
		out, err := NormalizeManualMemory(context.Background(), model, ManualMemoryInput{
			Text:    "Prefer morning meetings",
			Section: "Preferences",
		})
		if err == nil {
			t.Fatalf("expected error from failed model")
		}
		if out.Text != "Prefer morning meetings" || out.About != "the user" || out.Section != "Preferences" {
			t.Fatalf("unexpected fallback on error: %+v", out)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		model := &scriptedReplyModel{}
		model.SetHandler(func(context.Context, ReplyRequest) (Reply, error) {
			return Reply{Text: "not valid json"}, nil
		})
		out, err := NormalizeManualMemory(context.Background(), model, ManualMemoryInput{
			Text: "Allergic to peanuts",
		})
		if err == nil {
			t.Fatalf("expected error on invalid json")
		}
		if out.Text != "Allergic to peanuts" || out.About != "the user" {
			t.Fatalf("unexpected fallback on invalid json: %+v", out)
		}
	})
}
