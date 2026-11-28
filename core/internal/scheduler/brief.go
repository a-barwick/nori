package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/connections"
	"nori/internal/state"
)

const (
	maxCheckpointKeys = 200
)

// ParseProactiveOutcome parses a typed proactive outcome JSON payload
// ({outcome: "no_op" | "update" | "alert", finding_keys: []string, summary?: string, ...})
// from a task result string. It tolerates fenced ```json blocks.
func ParseProactiveOutcome(raw string) (ProactiveOutcome, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ProactiveOutcome{}, false
	}
	candidate := extractJSONObject(trimmed)
	if candidate != "" {
		var out ProactiveOutcome
		if err := json.Unmarshal([]byte(candidate), &out); err == nil {
			kind := OutcomeKind(strings.ToLower(strings.TrimSpace(string(out.Outcome))))
			switch kind {
			case OutcomeNoOp, OutcomeUpdate, OutcomeAlert:
				out.Outcome = kind
				out.FindingKeys = normalizeFindingKeys(out.FindingKeys)
				out.Summary = strings.TrimSpace(out.Summary)
				return out, true
			}
		}
	}
	return ProactiveOutcome{}, false
}

func extractJSONObject(s string) string {
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "```") {
		if nl := strings.IndexByte(trimmed, '\n'); nl >= 0 {
			rest := trimmed[nl+1:]
			if end := strings.LastIndex(rest, "```"); end >= 0 {
				trimmed = strings.TrimSpace(rest[:end])
			}
		}
	}
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
		return trimmed
	}
	start := strings.IndexByte(trimmed, '{')
	end := strings.LastIndexByte(trimmed, '}')
	if start >= 0 && end > start {
		return strings.TrimSpace(trimmed[start : end+1])
	}
	return ""
}

func normalizeFindingKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

func allKeysCheckpointed(keys, checkpoint []string) bool {
	if len(keys) == 0 || len(checkpoint) == 0 {
		return false
	}
	known := make(map[string]struct{}, len(checkpoint))
	for _, k := range checkpoint {
		known[k] = struct{}{}
	}
	for _, k := range keys {
		if _, ok := known[k]; !ok {
			return false
		}
	}
	return true
}

func mergeCheckpointKeys(existing, incoming []string) []string {
	merged := make([]string, 0, len(existing)+len(incoming))
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	for _, k := range existing {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, ok := seen[k]; !ok {
			seen[k] = struct{}{}
			merged = append(merged, k)
		}
	}
	for _, k := range incoming {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if _, ok := seen[k]; !ok {
			seen[k] = struct{}{}
			merged = append(merged, k)
		}
	}
	if len(merged) > maxCheckpointKeys {
		merged = merged[len(merged)-maxCheckpointKeys:]
	}
	return merged
}

// EnsureMorningBrief creates or updates the system Morning Brief automation
// (fixed ID auto_morning_brief) so its schedule, timezone tracking, execution
// window, and enabled/paused state match the user's preferences.
func (s *Service) EnsureMorningBrief(ctx context.Context, prefs connections.Preferences) (Automation, error) {
	pro := prefs.EffectiveProactivity()
	briefTime := pro.MorningBriefTime
	if _, _, ok := ParseHHMM(briefTime); !ok {
		briefTime = connections.DefaultMorningBriefTime
	}
	now := s.now()
	zone := strings.TrimSpace(prefs.Timezone)
	if zone == "" {
		zone = s.userTimezone(ctx)
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		loc = time.UTC
	}
	localStart := now.In(loc).Format("2006-01-02") + "T" + briefTime
	desiredStatus := AutomationEnabled
	if !pro.IsMorningBriefEnabled() {
		desiredStatus = AutomationPaused
	}

	var existingRev int64
	if cur, err := s.Store.Resource(ctx, CollectionAutomations, MorningBriefAutomationID); err == nil {
		existingRev = cur.Revision
	}
	reqID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("ensure-morning-brief:%d:%s:%s:%s", existingRev, briefTime, desiredStatus, zone))).String()
	receipt, err := s.arm(ctx)(s.Store.Mutate(ctx, state.Mutation{
		RequestID: reqID, Method: "PUT", Target: "/v1/automations/" + MorningBriefAutomationID,
		Collection: CollectionAutomations, ID: MorningBriefAutomationID, SkipRevisionCheck: true,
		Payload: map[string]any{"revision": existingRev, "time": briefTime, "status": desiredStatus, "zone": zone},
		Apply: func(current *state.Resource, _ func(string, string) (*state.Resource, error)) (state.MutationResult, error) {
			var a stored
			revision := int64(1)
			if current != nil {
				var err error
				if a, err = decodeAutomation(*current); err != nil {
					return state.MutationResult{}, err
				}
				revision = current.Revision + 1
				a.Generation++
			} else {
				a.Cursor = &now
			}
			a.ID = MorningBriefAutomationID
			a.Title = "Morning Brief"
			if strings.TrimSpace(a.Instruction) == "" {
				a.Instruction = "Prepare a concise Morning Brief summarizing today's schedule, priority messages, and pending items."
			}
			a.System = true
			a.Status = desiredStatus
			if desiredStatus == AutomationEnabled {
				a.PausedReason = ""
			}
			if a.BudgetUSD <= 0 {
				a.BudgetUSD = 0.15
			}
			a.Trigger = Trigger{
				Kind:               "recurring",
				LocalStart:         localStart,
				Timezone:           zone,
				FollowUserTimezone: true,
				RRule:              "FREQ=DAILY",
			}
			// Avoid firing a second Morning Brief on the same local calendar day if
			// the user changes the brief time or timezone after today's brief already ran.
			if a.Cursor != nil && a.Cursor.In(loc).Format("2006-01-02") == now.In(loc).Format("2006-01-02") && a.LastOccurrenceID != "" {
				tomorrow := now.In(loc).AddDate(0, 0, 1)
				startOfTomorrow, _ := LocalTime(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, loc)
				cursorUTC := startOfTomorrow.UTC().Add(-time.Second)
				a.Cursor = &cursorUTC
			}
			a.MaxActiveRuns = 1
			a.MissedRun = "skip"
			a.ExecutionWindowSeconds = DefaultMorningBriefExecutionWindowSeconds
			a.DestinationIDs = []string{ChatDestination}
			a.ConnectionIDs = nonNil(a.ConnectionIDs)
			a.AuthorityRuleIDs = nonNil(a.AuthorityRuleIDs)
			a.NextAt = s.computeNext(ctx, a, now)
			resource, err := state.NewResource(CollectionAutomations, MorningBriefAutomationID, revision, a, map[string]string{"status": string(a.Status)}, now)
			if err != nil {
				return state.MutationResult{}, err
			}
			a.Revision = revision
			data, _ := json.Marshal(a.Automation)
			return state.MutationResult{
				Writes:  []state.Resource{resource},
				Receipt: state.MutationReceipt{Status: "applied", Resource: data, HTTPStatus: 200},
			}, nil
		},
	}))
	if err != nil {
		return Automation{}, err
	}
	var out Automation
	if err := json.Unmarshal(receipt.Resource, &out); err != nil {
		return s.Get(ctx, MorningBriefAutomationID)
	}
	return out, nil
}

// --- Anti-Nagging (per finding_key) ---

func findingStateID(key string) string {
	return "fnd_" + strings.ReplaceAll(uuid.NewSHA1(uuid.NameSpaceOID, []byte("finding:"+strings.TrimSpace(key))).String(), "-", "")[:24]
}

// RecordFindingEngagement explicitly marks a finding_key as engaged (e.g. via
// "interaction", "readmark", or "user_turn").
func (s *Service) RecordFindingEngagement(ctx context.Context, key, via string) error {
	key = strings.TrimSpace(key)
	if key == "" || s.Store == nil {
		return nil
	}
	id := findingStateID(key)
	now := s.now()
	for attempt := 0; attempt < maxAutomationUpdateRetries; attempt++ {
		var fs FindingState
		var expected int64
		r, err := s.Store.Resource(ctx, CollectionFindings, id)
		if err == nil {
			expected = r.Revision
			if err := r.Decode(&fs); err != nil {
				return err
			}
		} else if !errors.Is(err, state.ErrNotFound) {
			return err
		} else {
			fs = FindingState{Key: key, FirstSurfacedAt: now, LastSurfacedAt: now}
		}
		fs.EngagedAt = &now
		fs.EngagedVia = via
		fs.Dropped = false
		res, err := state.NewResource(CollectionFindings, id, 0, fs, map[string]string{"key": key}, now)
		if err != nil {
			return err
		}
		if _, err := s.Store.PutResource(ctx, res, expected); err == nil {
			return nil
		} else if !errors.Is(err, state.ErrRevisionConflict) {
			return err
		}
	}
	return state.ErrRevisionConflict
}

func (s *Service) getFindingState(ctx context.Context, key string) (FindingState, int64, bool, error) {
	r, err := s.Store.Resource(ctx, CollectionFindings, findingStateID(key))
	if errors.Is(err, state.ErrNotFound) {
		return FindingState{}, 0, false, nil
	}
	if err != nil {
		return FindingState{}, 0, false, err
	}
	var fs FindingState
	if err := r.Decode(&fs); err != nil {
		return FindingState{}, 0, false, err
	}
	return fs, r.Revision, true, nil
}

// isFindingEngaged checks whether the user has engaged with a surfaced finding
// via interaction resolution, readmark advancement, or a user turn after surfacing.
func (s *Service) isFindingEngaged(ctx context.Context, fs FindingState) (bool, string) {
	if fs.EngagedAt != nil {
		return true, fs.EngagedVia
	}
	if fs.InteractionID != "" && s.Permissions != nil {
		if inter, err := s.Permissions.Interaction(ctx, fs.InteractionID); err == nil && inter.Status == "resolved" {
			return true, "interaction"
		}
	}
	if s.ReadPosition != nil && !fs.LastSurfacedAt.IsZero() {
		if mark, err := s.ReadPosition(ctx); err == nil && !mark.Through.IsZero() && !mark.Through.Before(fs.LastSurfacedAt) {
			return true, "readmark"
		}
	}
	if s.LastUserTurnAt != nil && !fs.FirstSurfacedAt.IsZero() {
		if turnAt, err := s.LastUserTurnAt(ctx); err == nil && turnAt.After(fs.FirstSurfacedAt) {
			return true, "user_turn"
		}
	}
	return false, ""
}

// shouldDropForAntiNag returns true when every finding_key on an item has
// already been folded into a Morning Brief at least once without user engagement.
func (s *Service) shouldDropForAntiNag(ctx context.Context, keys []string, interactionID string) (bool, error) {
	keys = normalizeFindingKeys(keys)
	if len(keys) == 0 {
		return false, nil
	}
	allExhausted := true
	for _, key := range keys {
		fs, _, exists, err := s.getFindingState(ctx, key)
		if err != nil {
			return false, err
		}
		if !exists {
			allExhausted = false
			continue
		}
		if fs.InteractionID == "" && interactionID != "" {
			fs.InteractionID = interactionID
		}
		if engaged, via := s.isFindingEngaged(ctx, fs); engaged {
			if fs.EngagedAt == nil {
				_ = s.RecordFindingEngagement(ctx, key, via)
			}
			allExhausted = false
			continue
		}
		if fs.BriefFoldCount < 1 {
			allExhausted = false
		}
	}
	return allExhausted, nil
}

func (s *Service) recordFindingSurfaced(ctx context.Context, keys []string, interactionID string, now time.Time, foldedIntoBrief bool) error {
	keys = normalizeFindingKeys(keys)
	for _, key := range keys {
		id := findingStateID(key)
		for attempt := 0; attempt < maxAutomationUpdateRetries; attempt++ {
			fs, rev, exists, err := s.getFindingState(ctx, key)
			if err != nil {
				return err
			}
			if !exists {
				fs = FindingState{
					Key:             key,
					FirstSurfacedAt: now,
					LastSurfacedAt:  now,
					InteractionID:   interactionID,
				}
			} else {
				fs.LastSurfacedAt = now
				if interactionID != "" && fs.InteractionID == "" {
					fs.InteractionID = interactionID
				}
			}
			if foldedIntoBrief {
				fs.BriefFoldCount++
			}
			res, err := state.NewResource(CollectionFindings, id, 0, fs, map[string]string{"key": key}, now)
			if err != nil {
				return err
			}
			if _, err := s.Store.PutResource(ctx, res, rev); err == nil {
				break
			} else if !errors.Is(err, state.ErrRevisionConflict) {
				return err
			}
		}
	}
	return nil
}

func (s *Service) markFindingDropped(ctx context.Context, keys []string, now time.Time) {
	for _, key := range normalizeFindingKeys(keys) {
		id := findingStateID(key)
		fs, rev, exists, err := s.getFindingState(ctx, key)
		if err != nil || !exists {
			continue
		}
		fs.Dropped = true
		if res, err := state.NewResource(CollectionFindings, id, 0, fs, map[string]string{"key": key}, now); err == nil {
			_, _ = s.Store.PutResource(ctx, res, rev)
		}
	}
}

// BuildBriefMessage constructs a PartBrief assistant message for Chat delivery
// with zero outer-loop turn, storing only interaction_ids and plain-text Fallback.
func BuildBriefMessage(a stored, o occurrenceDoc, outcome ProactiveOutcome, summary string, held []Delivery, now time.Time, zone string) state.Message {
	loc, err := time.LoadLocation(strings.TrimSpace(zone))
	if err != nil || loc == nil {
		loc = time.UTC
	}
	localNow := now.In(loc)

	greeting := strings.TrimSpace(outcome.Greeting)
	if greeting == "" {
		greeting = "Good morning"
	}
	meta := strings.TrimSpace(outcome.Meta)
	if meta == "" {
		meta = localNow.Format("Monday, Jan 2")
	}

	items := make([]state.BriefItem, 0, len(outcome.Items)+len(held)+1)
	for _, it := range outcome.Items {
		items = append(items, state.BriefItem{
			Title:         strings.TrimSpace(it.Title),
			Trailing:      strings.TrimSpace(it.Trailing),
			InteractionID: strings.TrimSpace(it.InteractionID),
			ActionLabel:   strings.TrimSpace(it.ActionLabel),
		})
	}
	for _, h := range held {
		title := strings.TrimSpace(h.Title)
		if title == "" {
			title = "Overnight update"
		}
		trailing := clip(strings.TrimSpace(h.Body), 120)
		item := state.BriefItem{
			Title:         title,
			Trailing:      trailing,
			InteractionID: strings.TrimSpace(h.InteractionID),
		}
		if item.InteractionID != "" {
			item.ActionLabel = "Review"
		}
		items = append(items, item)
	}
	if len(items) == 0 && strings.TrimSpace(summary) != "" {
		items = append(items, state.BriefItem{
			Title: clip(strings.TrimSpace(summary), 200),
		})
	}

	var fallbackLines []string
	fallbackLines = append(fallbackLines, fmt.Sprintf("%s — %s", greeting, meta))
	if len(items) == 0 && strings.TrimSpace(summary) != "" {
		fallbackLines = append(fallbackLines, strings.TrimSpace(summary))
	} else {
		for _, it := range items {
			line := "• " + it.Title
			if it.Trailing != "" {
				line += " — " + it.Trailing
			}
			fallbackLines = append(fallbackLines, line)
		}
	}
	fallback := strings.Join(fallbackLines, "\n")

	return state.Message{
		Role:      "assistant",
		Status:    state.MessageStatusCompleted,
		Text:      fallback,
		CreatedAt: now,
		Parts: []state.Part{{
			Type:     state.PartBrief,
			Greeting: greeting,
			Meta:     meta,
			Items:    items,
			Fallback: fallback,
		}},
	}
}

// deliverMorningBrief delivers a Morning Brief as a PartBrief message to Chat
// (zero outer-loop turn) and folds held quiet-hours deliveries ONLY AFTER brief
// delivery succeeds, respecting anti-nagging rules.
func (s *Service) deliverMorningBrief(
	ctx context.Context,
	a stored,
	o occurrenceDoc,
	run state.Run,
	outcome ProactiveOutcome,
	summary string,
	results []string,
) ([]string, error) {
	now := s.now()
	heldRes, heldAll, err := s.heldDeliveryResources(ctx)
	if err != nil {
		return nil, err
	}

	// Partition held deliveries into those eligible to fold vs those dropped by anti-nagging.
	type heldPair struct {
		res state.Resource
		d   Delivery
	}
	var toFold []heldPair
	var toDrop []heldPair
	for i, h := range heldAll {
		drop, err := s.shouldDropForAntiNag(ctx, h.FindingKeys, h.InteractionID)
		if err != nil {
			slog.WarnContext(ctx, "scheduler anti-nag check failed", "delivery_id", h.ID, "err", err)
		}
		if drop {
			toDrop = append(toDrop, heldPair{res: heldRes[i], d: h})
		} else {
			toFold = append(toFold, heldPair{res: heldRes[i], d: h})
		}
	}

	// Sort folded items chronologically so brief ordering is deterministic.
	slices.SortStableFunc(toFold, func(x, y heldPair) int {
		if x.d.CreatedAt == nil || y.d.CreatedAt == nil {
			return strings.Compare(x.d.ID, y.d.ID)
		}
		return x.d.CreatedAt.Compare(*y.d.CreatedAt)
	})
	foldedDeliveries := make([]Delivery, 0, len(toFold))
	for _, p := range toFold {
		foldedDeliveries = append(foldedDeliveries, p.d)
	}

	destID := ChatDestination
	id := deliveryID(o.ID, destID)
	zone := s.userTimezone(ctx)
	briefMsg := BuildBriefMessage(a, o, outcome, summary, foldedDeliveries, now, zone)

	d := Delivery{
		ID:            id,
		OccurrenceID:  o.ID,
		AutomationID:  a.ID,
		ResultIDs:     results,
		TaskID:        run.ID,
		DestinationID: destID,
		State:         DeliveryStatePending,
		Title:         a.Title,
		Body:          briefMsg.Text,
		Outcome:       outcome.Outcome,
		FindingKeys:   outcome.FindingKeys,
		CreatedAt:     &now,
	}

	// Deliver PartBrief message directly to Chat (zero outer-loop turn).
	if s.AppendProactiveMessage != nil {
		appended, err := s.AppendProactiveMessage(ctx, id, briefMsg)
		if err != nil {
			return nil, err
		}
		d.State = DeliveryStateDelivered
		d.Receipt = appended.ID
	} else {
		// Fallback to in-app inbox if AppendProactiveMessage is not wired.
		if err := s.deliverInboxItem(ctx, &d, now); err != nil {
			return nil, err
		}
	}
	d.UpdatedAt = &now
	res, err := state.NewResource(CollectionDeliveries, id, 0, d, map[string]string{"occurrence_id": o.ID, "state": d.State}, now)
	if err != nil {
		return nil, err
	}
	if _, err := s.Store.PutResource(ctx, res, 0); err != nil && !errors.Is(err, state.ErrRevisionConflict) {
		return nil, err
	}

	if d.State == DeliveryStateDelivered && s.OnPush != nil {
		_ = s.OnPush(ctx, d)
	}

	// ONLY AFTER brief delivery succeeds: mark included held deliveries as folded
	// (receipt = brief occurrence ID) and record their brief fold count only when
	// the state transition actually succeeds.
	for _, p := range toFold {
		h := p.d
		rev := p.res.Revision
		folded := false
		for attempt := 0; attempt < 2; attempt++ {
			h.State = DeliveryStateFolded
			h.Receipt = o.ID
			h.UpdatedAt = &now
			nextRes, err := state.NewResource(CollectionDeliveries, h.ID, 0, h, map[string]string{"occurrence_id": h.OccurrenceID, "state": DeliveryStateFolded}, now)
			if err != nil {
				break
			}
			if _, err := s.Store.PutResource(ctx, nextRes, rev); err == nil {
				folded = true
				break
			} else if errors.Is(err, state.ErrRevisionConflict) {
				freshRes, rerr := s.Store.Resource(ctx, CollectionDeliveries, h.ID)
				if rerr != nil {
					break
				}
				var fresh Delivery
				if freshRes.Decode(&fresh) != nil || fresh.State != DeliveryStateHeld {
					break
				}
				h = fresh
				rev = freshRes.Revision
				continue
			} else {
				slog.WarnContext(ctx, "scheduler mark folded failed", "delivery_id", h.ID, "err", err)
				break
			}
		}
		if folded {
			if err := s.recordFindingSurfaced(ctx, h.FindingKeys, h.InteractionID, now, true); err != nil {
				slog.WarnContext(ctx, "scheduler record brief fold failed", "delivery_id", h.ID, "err", err)
			}
		}
	}

	// Mark anti-nag dropped items as dropped so they don't linger as held.
	for _, p := range toDrop {
		h := p.d
		h.State = DeliveryStateDropped
		h.Receipt = "anti_nag"
		h.UpdatedAt = &now
		nextRes, err := state.NewResource(CollectionDeliveries, h.ID, 0, h, map[string]string{"occurrence_id": h.OccurrenceID, "state": DeliveryStateDropped}, now)
		if err != nil {
			continue
		}
		_, _ = s.Store.PutResource(ctx, nextRes, p.res.Revision)
		s.markFindingDropped(ctx, h.FindingKeys, now)
	}

	return []string{id}, nil
}
