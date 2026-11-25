package conversation

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"nori/internal/agent"
	"nori/internal/llmguard"
	"nori/internal/prompts"
	"nori/internal/state"
	"nori/internal/state/docstore"
	"nori/internal/transcript"
	"nori/internal/usage"
)

const manualMemoryNormalizeTimeout = 10 * time.Second

// ManualMemoryInput carries the raw manual memory submission and existing Core memories
// for lightweight AI normalization & deduplication.
type ManualMemoryInput struct {
	Text     string
	About    string
	Section  string
	Existing []state.Memory
}

// NormalizedMemory is the normalized result of a manual memory addition.
type NormalizedMemory struct {
	Text       string
	About      string
	Section    string
	ReplacesID string
}

// NewMemoryNormalizer returns a lightweight AI normalizer backed by an outer ReplyModel.
// When model is nil or the call fails/times out, callers fall back cleanly to raw fields.
func NewMemoryNormalizer(model ReplyModel) func(ctx context.Context, in ManualMemoryInput) (NormalizedMemory, error) {
	return NewMemoryNormalizerWithMeter(model, nil)
}

// NewMemoryNormalizerWithMeter wraps ReplyModel-based memory normalization with durable usage metering.
func NewMemoryNormalizerWithMeter(model ReplyModel, meter usage.Meter) func(ctx context.Context, in ManualMemoryInput) (NormalizedMemory, error) {
	if model == nil {
		return nil
	}
	return func(ctx context.Context, in ManualMemoryInput) (NormalizedMemory, error) {
		return normalizeManualMemoryWithMeter(ctx, model, meter, in)
	}
}

// NewSummaryModelMemoryNormalizer runs manual memory normalization on the lower-tier inner
// SummaryModel (which already carries durable usage metering), falling back to raw user text on error.
func NewSummaryModelMemoryNormalizer(model SummaryModel) func(ctx context.Context, in ManualMemoryInput) (NormalizedMemory, error) {
	if model == nil {
		return nil
	}
	return func(ctx context.Context, in ManualMemoryInput) (NormalizedMemory, error) {
		return normalizeManualMemoryWithSummaryModel(ctx, model, in)
	}
}

// NormalizeManualMemory cleans up phrasing into concise third-person form, infers section and
// about (respecting explicit caller overrides), and detects duplicate/updated existing memories.
func NormalizeManualMemory(ctx context.Context, model ReplyModel, in ManualMemoryInput) (NormalizedMemory, error) {
	return normalizeManualMemoryWithMeter(ctx, model, nil, in)
}

func normalizeManualMemoryWithMeter(ctx context.Context, model ReplyModel, meter usage.Meter, in ManualMemoryInput) (NormalizedMemory, error) {
	rawText := strings.TrimSpace(in.Text)
	explicitAbout := strings.TrimSpace(in.About)
	explicitSection := strings.TrimSpace(in.Section)
	fallback := NormalizedMemory{
		Text:    rawText,
		About:   cmp.Or(explicitAbout, "the user"),
		Section: explicitSection,
	}
	if model == nil || rawText == "" {
		return fallback, nil
	}

	payload, validExisting, err := buildNormalizeMemoryPayload(rawText, explicitAbout, explicitSection, in.Existing)
	if err != nil {
		return fallback, err
	}

	sysMsg := modelMessage(map[string]string{
		"role":    "system",
		"content": prompts.Runtime("normalize-memory", nil),
	})
	userMsg := modelMessage(map[string]string{
		"role":    "user",
		"content": string(payload),
	})

	normCtx, cancel := context.WithTimeout(llmguard.WithScope(ctx, llmguard.ScopeExtract), manualMemoryNormalizeTimeout)
	defer cancel()

	var recordFn func(context.Context, state.Usage) error
	if meter != nil {
		recordFn = func(rctx context.Context, u state.Usage) error {
			if u.InputID == "" {
				u.InputID = "memory-normalize"
			}
			return meter.RecordUsage(rctx, u)
		}
	}

	reply, err := model.Respond(normCtx, ReplyRequest{
		Messages:       []json.RawMessage{sysMsg, userMsg},
		NoDispatch:     true,
		RecordUsage:    recordFn,
		TelemetryInput: "manual-memory-normalize",
	})
	if meter != nil && reply.Usage.ID != "" {
		if reply.Usage.InputID == "" {
			reply.Usage.InputID = "memory-normalize"
		}
		_ = usage.Finish(ctx, meter, reply.Usage)
	}
	if err != nil {
		slog.WarnContext(ctx, "manual memory normalizer failed; using raw input", "err", err)
		return fallback, err
	}

	return finishNormalizedMemory(ctx, reply.Text, rawText, explicitAbout, explicitSection, validExisting, fallback)
}

func normalizeManualMemoryWithSummaryModel(ctx context.Context, model SummaryModel, in ManualMemoryInput) (NormalizedMemory, error) {
	rawText := strings.TrimSpace(in.Text)
	explicitAbout := strings.TrimSpace(in.About)
	explicitSection := strings.TrimSpace(in.Section)
	fallback := NormalizedMemory{
		Text:    rawText,
		About:   cmp.Or(explicitAbout, "the user"),
		Section: explicitSection,
	}
	if model == nil || rawText == "" {
		return fallback, nil
	}

	payload, validExisting, err := buildNormalizeMemoryPayload(rawText, explicitAbout, explicitSection, in.Existing)
	if err != nil {
		return fallback, err
	}

	normCtx, cancel := context.WithTimeout(
		usage.WithScope(llmguard.WithScope(ctx, llmguard.ScopeExtract), "memory-normalize", ""),
		manualMemoryNormalizeTimeout,
	)
	defer cancel()

	resp, err := model.Respond(normCtx, agent.Request{
		Instructions:    prompts.Runtime("normalize-memory", nil),
		Messages:        []transcript.Message{transcript.UserText(string(payload))},
		MaxOutputTokens: 1024,
		Reasoning:       "low",
		SingleTurn:      true,
	})
	if err != nil {
		slog.WarnContext(ctx, "manual memory normalizer failed; using raw input", "err", err)
		return fallback, err
	}
	return finishNormalizedMemory(ctx, resp.Answer, rawText, explicitAbout, explicitSection, validExisting, fallback)
}

func buildNormalizeMemoryPayload(rawText, explicitAbout, explicitSection string, allExisting []state.Memory) ([]byte, map[string]state.Memory, error) {
	existing := userProvenanceMemories(allExisting)
	existing = slices.Clone(existing)
	state.SortCoreMemories(existing)
	validExisting := make(map[string]state.Memory, len(existing))
	type existingItem struct {
		ID      string `json:"id"`
		Text    string `json:"text"`
		About   string `json:"about,omitempty"`
		Section string `json:"section,omitempty"`
	}
	items := make([]existingItem, 0, len(existing))
	for _, m := range existing {
		validExisting[m.ID] = m
		items = append(items, existingItem{
			ID:      m.ID,
			Text:    m.Text,
			About:   m.About,
			Section: m.Section,
		})
	}
	payload, err := json.Marshal(map[string]any{
		"raw_text":          rawText,
		"explicit_about":    explicitAbout,
		"explicit_section":  explicitSection,
		"existing_memories": items,
	})
	return payload, validExisting, err
}

func finishNormalizedMemory(ctx context.Context, rawReply, rawText, explicitAbout, explicitSection string, validExisting map[string]state.Memory, fallback NormalizedMemory) (NormalizedMemory, error) {
	parsed, err := parseNormalizedMemory(rawReply)
	if err != nil {
		slog.WarnContext(ctx, "manual memory normalizer returned invalid JSON; using raw input", "err", err)
		return fallback, err
	}

	out := NormalizedMemory{
		Text:    strings.TrimSpace(parsed.Text),
		About:   strings.TrimSpace(parsed.About),
		Section: strings.TrimSpace(parsed.Section),
	}
	if out.Text == "" || len(out.Text) > state.MaxMemoryText || !utf8.ValidString(out.Text) {
		out.Text = rawText
	}
	if explicitAbout != "" {
		out.About = explicitAbout
	} else {
		if !utf8.ValidString(out.About) {
			out.About = ""
		}
		out.About = docstore.ClipUTF8(cmp.Or(out.About, "the user"), state.MaxMemoryAbout)
	}
	if explicitSection != "" {
		out.Section = explicitSection
	} else {
		if !utf8.ValidString(out.Section) {
			out.Section = ""
		}
		out.Section = docstore.ClipUTF8(out.Section, state.MaxMemorySection)
	}
	if parsed.Replaces != nil {
		id := strings.TrimSpace(*parsed.Replaces)
		if _, ok := validExisting[id]; ok {
			out.ReplacesID = id
		}
	}
	return out, nil
}

type normalizedMemoryJSON struct {
	Text     string  `json:"text"`
	About    string  `json:"about"`
	Section  string  `json:"section"`
	Replaces *string `json:"replaces"`
}

func parseNormalizedMemory(raw string) (normalizedMemoryJSON, error) {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "```") {
		if idx := strings.Index(trimmed, "\n"); idx >= 0 {
			trimmed = trimmed[idx+1:]
		}
		if end := strings.LastIndex(trimmed, "```"); end >= 0 {
			trimmed = trimmed[:end]
		}
		trimmed = strings.TrimSpace(trimmed)
	}
	if start := strings.Index(trimmed, "{"); start >= 0 {
		if end := strings.LastIndex(trimmed, "}"); end > start {
			trimmed = trimmed[start : end+1]
		}
	}
	var out normalizedMemoryJSON
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return normalizedMemoryJSON{}, err
	}
	if strings.TrimSpace(out.Text) == "" {
		return normalizedMemoryJSON{}, errors.New("normalized memory text is empty")
	}
	return out, nil
}
