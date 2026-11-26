package invariants

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/permissions"
	"nori/internal/scheduler"
	"nori/internal/sim/memstore"
	"nori/internal/state"
	"nori/internal/state/docstore"
)

func (c *Checker) CheckSafety(ctx context.Context, users []UserTarget) error {
	return c.CheckSafetyAt(ctx, users, c.now())
}

func (c *Checker) CheckSafetyAt(ctx context.Context, users []UserTarget, now time.Time) error {
	now = now.UTC()
	sorted := sortUsers(users)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.safetyChecks++

	var vs Violations

	for _, ev := range c.pausedStarts {
		vs = append(vs, Violation{
			Invariant: "control.execution_started_while_paused",
			UserID:    ev.userID,
			EntityID:  ev.runID,
			Message:   fmt.Sprintf("new workflow execution started while Control.Paused was true (at %s)", ev.at.Format(time.RFC3339Nano)),
		})
	}
	vs = append(vs, c.stickyViolations...)

	if c.backend != nil {
		snap := c.backend.Snapshot()
		vs = append(vs, c.checkSafetyFromSnapshotLocked(ctx, sorted, snap, now)...)
	} else {
		vs = append(vs, c.checkSafetyFromStoresLocked(ctx, sorted, now)...)
	}

	if len(vs) > 0 {
		return vs
	}
	return nil
}

type userSnapshotData struct {
	target       UserTarget
	userID       string
	owner        string
	runs         map[string]state.Run
	taskControls map[string]state.TaskControl
	opsByRun     map[string][]rawOperationRecord
	cmds         []state.Command
	msgs         []state.Message
	interactions []rawInteractionRecord
	automations  map[string]storedAutomation
	occurrences  []storedOccurrence
}

func (c *Checker) checkSafetyFromSnapshotLocked(ctx context.Context, targets []UserTarget, snap memstore.BackendSnapshot, now time.Time) []Violation {
	var vs []Violation

	_, ctrlVs := decodeControlFromSnapshot(snap)
	vs = append(vs, ctrlVs...)

	knownOwners := make(map[string]string, len(targets))
	knownUsers := make(map[string]bool, len(targets))
	for _, t := range targets {
		knownUsers[t.User.ID] = true
		owner := t.User.DataOwner()
		if prevUser, dup := knownOwners[owner]; dup && prevUser != t.User.ID {
			vs = append(vs, Violation{
				Invariant: "isolation.duplicate_data_owner",
				UserID:    t.User.ID,
				EntityID:  owner,
				Message:   fmt.Sprintf("users %q and %q share DataOwner %q", prevUser, t.User.ID, owner),
			})
		}
		knownOwners[owner] = t.User.ID
	}

	for _, lease := range snap.TaskLeases {
		if !knownUsers[lease.UserID] {
			vs = append(vs, Violation{
				Invariant: "isolation.unknown_lease_user",
				UserID:    lease.UserID,
				EntityID:  lease.RunID,
				Message:   fmt.Sprintf("task lease (%s, %s) does not belong to any known user", lease.UserID, lease.RunID),
			})
		}
	}
	for _, w := range snap.UserWakes {
		if !knownUsers[w.UserID] {
			vs = append(vs, Violation{
				Invariant: "isolation.unknown_wake_user",
				UserID:    w.UserID,
				EntityID:  w.Lane,
				Message:   fmt.Sprintf("user wake (%s, %s) does not belong to any known user", w.UserID, w.Lane),
			})
		}
	}

	for _, col := range sortedKeys(snap.Collections) {
		if col == "owners" {
			for _, id := range sortedKeys(snap.Collections[col]) {
				if _, ok := knownOwners[id]; !ok {
					vs = append(vs, Violation{
						Invariant: "isolation.unknown_owner_path",
						EntityID:  "owners/" + id,
						Message:   fmt.Sprintf("document under owners/%s does not belong to any known user", id),
					})
				}
			}
			continue
		}
		if owner, ok := extractOwnerFromPath(col); ok {
			if _, known := knownOwners[owner]; !known {
				vs = append(vs, Violation{
					Invariant: "isolation.unknown_owner_path",
					EntityID:  col,
					Message:   fmt.Sprintf("collection %q belongs to unknown owner %q", col, owner),
				})
			}
		}
	}
	for _, seqPath := range sortedKeys(snap.Sequences) {
		if owner, ok := extractOwnerFromPath(seqPath); ok {
			if _, known := knownOwners[owner]; !known {
				vs = append(vs, Violation{
					Invariant: "isolation.unknown_owner_path",
					EntityID:  seqPath,
					Message:   fmt.Sprintf("sequence %q belongs to unknown owner %q", seqPath, owner),
				})
			}
		}
	}

	dataList := make([]userSnapshotData, 0, len(targets))
	for _, t := range targets {
		owner := t.User.DataOwner()
		uData := userSnapshotData{
			target:   t,
			userID:   t.User.ID,
			owner:    owner,
			opsByRun: map[string][]rawOperationRecord{},
		}

		runs, runVs := decodeRunsFromSnapshot(snap, owner)
		vs = append(vs, tagUser(t.User.ID, runVs)...)
		uData.runs = runs

		taskControls, tcVs := decodeTaskControlsFromSnapshot(snap, owner)
		vs = append(vs, tagUser(t.User.ID, tcVs)...)
		uData.taskControls = taskControls

		opPrefix := "owners/" + owner + "/runs/"
		opSuffix := "/operations"
		opRunIDs := map[string]bool{}
		for runID := range runs {
			opRunIDs[runID] = true
		}
		for _, col := range sortedKeys(snap.Collections) {
			if strings.HasPrefix(col, opPrefix) && strings.HasSuffix(col, opSuffix) {
				mid := strings.TrimSuffix(strings.TrimPrefix(col, opPrefix), opSuffix)
				if mid != "" && !strings.Contains(mid, "/") {
					opRunIDs[mid] = true
				}
			}
		}
		for _, runID := range sortedKeys(opRunIDs) {
			ops, opVs := decodeOperationsFromSnapshot(snap, owner, runID)
			vs = append(vs, tagUser(t.User.ID, opVs)...)
			if len(ops) > 0 {
				uData.opsByRun[runID] = ops
			}
		}

		cmds, cmdVs := decodeCommandsFromSnapshot(snap, owner)
		vs = append(vs, tagUser(t.User.ID, cmdVs)...)
		uData.cmds = cmds

		msgs, msgVs := decodeMessagesFromSnapshot(snap, owner)
		vs = append(vs, tagUser(t.User.ID, msgVs)...)
		uData.msgs = msgs

		inters, interVs := decodeInteractionsFromSnapshot(snap, owner)
		vs = append(vs, tagUser(t.User.ID, interVs)...)
		uData.interactions = inters

		autos, autoVs := decodeAutomationsFromSnapshot(snap, owner)
		vs = append(vs, tagUser(t.User.ID, autoVs)...)
		uData.automations = autos

		occs, occVs := decodeOccurrencesFromSnapshot(snap, owner)
		vs = append(vs, tagUser(t.User.ID, occVs)...)
		uData.occurrences = occs

		for _, runID := range sortedKeys(uData.runs) {
			vs = append(vs, c.registerOwnerLocked(c.runOwners, "run", runID, t.User.ID)...)
		}
		for _, inter := range uData.interactions {
			vs = append(vs, c.registerOwnerLocked(c.interactionOwners, "interaction", inter.resource.ID, t.User.ID)...)
		}
		for _, autoID := range sortedKeys(uData.automations) {
			vs = append(vs, c.registerOwnerLocked(c.automationOwners, "automation", autoID, t.User.ID)...)
		}
		for _, occ := range uData.occurrences {
			vs = append(vs, c.registerOwnerLocked(c.occurrenceOwners, "occurrence", occ.ID, t.User.ID)...)
		}

		dataList = append(dataList, uData)
	}

	for _, lease := range snap.TaskLeases {
		if knownUsers[lease.UserID] {
			vs = append(vs, c.checkOwnedByLocked(c.runOwners, "run", lease.RunID, lease.UserID, "task_lease "+lease.RunID)...)
		}
	}

	for _, u := range dataList {
		eventPos, evVs := c.checkEventLogFromSnapshotLocked(u.userID, u.owner, snap)
		vs = append(vs, evVs...)

		mark, hasMark, err := decodeReadMarkFromSnapshot(snap, u.owner)
		if err != nil {
			vs = append(vs, Violation{
				Invariant: "readmark.decode",
				UserID:    u.userID,
				Message:   fmt.Sprintf("failed to decode read mark: %v", err),
			})
		} else if hasMark {
			vs = append(vs, c.checkReadMarkLocked(u.userID, mark, eventPos)...)
		}

		conv, hasConv, err := decodeConversationFromSnapshot(snap, u.owner)
		if err != nil {
			vs = append(vs, Violation{
				Invariant: "conversation.decode",
				UserID:    u.userID,
				Message:   fmt.Sprintf("failed to decode conversation control: %v", err),
			})
		} else if hasConv {
			vs = append(vs, c.checkConversationLocked(u.userID, conv)...)
		}

		vs = append(vs, c.checkRunsLocked(u.userID, u.runs, true)...)

		vs = append(vs, c.checkTaskControlsLocked(u.userID, u.taskControls)...)

		vs = append(vs, c.checkOperationsLocked(u.userID, u.runs, u.opsByRun, true)...)

		vs = append(vs, c.checkCommandsLocked(u.userID, u.runs, u.cmds)...)

		for _, msg := range u.msgs {
			if msg.RunID != "" {
				vs = append(vs, c.checkOwnedByLocked(c.runOwners, "run", msg.RunID, u.userID, "message "+msg.ID)...)
			}
		}

		permSvc := &permissions.Service{
			Store: &snapshotResourceStore{snap: snap, owner: u.owner},
			Now:   func() time.Time { return now },
		}
		vs = append(vs, c.checkInteractionsLocked(ctx, u.userID, u.runs, u.interactions, permSvc, now)...)

		vs = append(vs, c.checkAutomationsAndOccurrencesLocked(u.userID, u.automations, u.occurrences)...)
	}

	vs = append(vs, c.checkMemorySafetyFromSnapshotLocked(ctx, dataList, knownOwners, snap, now)...)

	return vs
}

func (c *Checker) checkSafetyFromStoresLocked(ctx context.Context, targets []UserTarget, now time.Time) []Violation {
	var vs []Violation

	type storeData struct {
		target       UserTarget
		userID       string
		runs         map[string]state.Run
		taskControls map[string]state.TaskControl
		opsByRun     map[string][]rawOperationRecord
		pendingCmds  []state.Command
		msgs         []state.Message
		interactions []rawInteractionRecord
		automations  map[string]storedAutomation
		occurrences  []storedOccurrence
		eventPos     int64
		readMark     state.ReadMark
		hasReadMark  bool
		conv         state.ConversationControl
	}

	dataList := make([]storeData, 0, len(targets))
	for _, t := range targets {
		u := storeData{
			target:       t,
			userID:       t.User.ID,
			runs:         map[string]state.Run{},
			taskControls: map[string]state.TaskControl{},
			opsByRun:     map[string][]rawOperationRecord{},
			automations:  map[string]storedAutomation{},
		}
		if t.Store == nil {
			continue
		}
		err := c.withoutFaults(func() error {
			sess, err := t.Store.Session(ctx)
			if err != nil {
				return fmt.Errorf("Session: %w", err)
			}
			u.msgs = sess.Messages
			for _, run := range sess.Runs {
				u.runs[run.ID] = run
			}
			open, err := t.Store.OpenTasks(ctx)
			if err != nil {
				return fmt.Errorf("OpenTasks: %w", err)
			}
			for _, run := range open {
				u.runs[run.ID] = run
			}
			for _, runID := range sortedKeys(u.runs) {
				if tc, err := t.Store.GetTaskControl(ctx, runID); err == nil {
					u.taskControls[runID] = tc
				} else if !errors.Is(err, state.ErrNotFound) && !errors.Is(err, docstore.ErrNotFound) {
					return fmt.Errorf("GetTaskControl(%s): %w", runID, err)
				}
				ops, err := t.Store.Operations(ctx, runID)
				if err != nil {
					return fmt.Errorf("Operations(%s): %w", runID, err)
				}
				for _, op := range ops {
					recorded := op.CallID != "" && op.Name != ""
					u.opsByRun[runID] = append(u.opsByRun[runID], rawOperationRecord{
						docID:            op.ID,
						op:               op,
						evidenceRecorded: recorded,
					})
				}
			}
			after := ""
			for {
				page, err := t.Store.Pending(ctx, after)
				if err != nil {
					return fmt.Errorf("Pending: %w", err)
				}
				u.pendingCmds = append(u.pendingCmds, page.Commands...)
				if page.Next == "" {
					break
				}
				after = page.Next
			}
			pos, err := t.Store.EventPosition(ctx)
			if err != nil {
				return fmt.Errorf("EventPosition: %w", err)
			}
			u.eventPos = pos

			mark, err := t.Store.ReadPosition(ctx)
			if err != nil {
				return fmt.Errorf("ReadPosition: %w", err)
			}
			u.readMark = mark
			u.hasReadMark = mark.MessageID != "" || mark.Position != 0 || !mark.Through.IsZero()

			conv, _, err := t.Store.ConversationStatus(ctx)
			if err != nil {
				return fmt.Errorf("ConversationStatus: %w", err)
			}
			u.conv = conv

			resAfter := ""
			for {
				page, next, err := t.Store.Resources(ctx, permissions.CollectionInteractions, nil, resAfter, 200)
				if err != nil {
					return fmt.Errorf("Resources(interactions): %w", err)
				}
				for _, r := range page {
					var inter permissions.Interaction
					if err := r.Decode(&inter); err != nil {
						return fmt.Errorf("decode interaction %s: %w", r.ID, err)
					}
					inter.Revision = r.Revision
					u.interactions = append(u.interactions, rawInteractionRecord{resource: r, interaction: inter})
				}
				if next == "" {
					break
				}
				resAfter = next
			}

			resAfter = ""
			for {
				page, next, err := t.Store.Resources(ctx, scheduler.CollectionAutomations, nil, resAfter, 200)
				if err != nil {
					return fmt.Errorf("Resources(automations): %w", err)
				}
				for _, r := range page {
					var a storedAutomation
					if err := r.Decode(&a); err != nil {
						return fmt.Errorf("decode automation %s: %w", r.ID, err)
					}
					a.Revision = r.Revision
					u.automations[r.ID] = a
				}
				if next == "" {
					break
				}
				resAfter = next
			}

			resAfter = ""
			for {
				page, next, err := t.Store.Resources(ctx, scheduler.CollectionOccurrences, nil, resAfter, 200)
				if err != nil {
					return fmt.Errorf("Resources(occurrences): %w", err)
				}
				for _, r := range page {
					var o storedOccurrence
					if err := r.Decode(&o); err != nil {
						return fmt.Errorf("decode occurrence %s: %w", r.ID, err)
					}
					u.occurrences = append(u.occurrences, o)
				}
				if next == "" {
					break
				}
				resAfter = next
			}
			return nil
		})
		if err != nil {
			vs = append(vs, Violation{
				Invariant: "store.read",
				UserID:    t.User.ID,
				Message:   err.Error(),
			})
			continue
		}

		for _, runID := range sortedKeys(u.runs) {
			vs = append(vs, c.registerOwnerLocked(c.runOwners, "run", runID, t.User.ID)...)
		}
		for _, inter := range u.interactions {
			vs = append(vs, c.registerOwnerLocked(c.interactionOwners, "interaction", inter.resource.ID, t.User.ID)...)
		}
		for _, autoID := range sortedKeys(u.automations) {
			vs = append(vs, c.registerOwnerLocked(c.automationOwners, "automation", autoID, t.User.ID)...)
		}
		for _, occ := range u.occurrences {
			vs = append(vs, c.registerOwnerLocked(c.occurrenceOwners, "occurrence", occ.ID, t.User.ID)...)
		}
		dataList = append(dataList, u)
	}

	for _, u := range dataList {
		vs = append(vs, c.checkEventPositionLocked(u.userID, u.eventPos)...)
		if u.hasReadMark {
			vs = append(vs, c.checkReadMarkLocked(u.userID, u.readMark, u.eventPos)...)
		}
		vs = append(vs, c.checkConversationLocked(u.userID, u.conv)...)
		vs = append(vs, c.checkRunsLocked(u.userID, u.runs, false)...)
		vs = append(vs, c.checkTaskControlsLocked(u.userID, u.taskControls)...)
		vs = append(vs, c.checkOperationsLocked(u.userID, u.runs, u.opsByRun, false)...)
		vs = append(vs, c.checkCommandsLocked(u.userID, u.runs, u.pendingCmds)...)
		for _, msg := range u.msgs {
			if msg.RunID != "" {
				vs = append(vs, c.checkOwnedByLocked(c.runOwners, "run", msg.RunID, u.userID, "message "+msg.ID)...)
			}
		}
		permSvc := u.target.Permissions
		if permSvc == nil && u.target.Store != nil {
			permSvc = &permissions.Service{Store: u.target.Store, Now: func() time.Time { return now }}
		}
		_ = c.withoutFaults(func() error {
			vs = append(vs, c.checkInteractionsLocked(ctx, u.userID, u.runs, u.interactions, permSvc, now)...)
			return nil
		})
		vs = append(vs, c.checkAutomationsAndOccurrencesLocked(u.userID, u.automations, u.occurrences)...)
	}

	return vs
}

func (c *Checker) checkEventLogFromSnapshotLocked(userID, owner string, snap memstore.BackendSnapshot) (int64, []Violation) {
	var vs []Violation

	headPath := "owners/" + owner + "/event_head/head"
	seqPos, hasSeq := snap.Sequences[headPath]

	var docPos int64
	var hasDoc bool
	if headCol, ok := snap.Collections["owners/"+owner+"/event_head"]; ok {
		if fields, ok := headCol["head"]; ok {
			hasDoc = true
			if v, ok := fields["seq"].(int64); ok {
				docPos = v
			}
		}
	}

	eventPos := seqPos
	if !hasSeq && hasDoc {
		eventPos = docPos
	} else if hasSeq && hasDoc && seqPos != docPos {
		vs = append(vs, Violation{
			Invariant: "event_log.head_mismatch",
			UserID:    userID,
			Message:   fmt.Sprintf("sequence head (%d) disagrees with event_head doc (%d)", seqPos, docPos),
		})
	}

	vs = append(vs, c.checkEventPositionLocked(userID, eventPos)...)

	eventDocs := snap.Collections["owners/"+owner+"/events"]
	if eventPos == 0 {
		if len(eventDocs) > 0 {
			vs = append(vs, Violation{
				Invariant: "event_log.unexpected_events",
				UserID:    userID,
				Message:   fmt.Sprintf("EventPosition is 0 but found %d stored event document(s)", len(eventDocs)),
			})
		}
		return eventPos, vs
	}

	seenSeq := make(map[int64]bool, len(eventDocs))
	for _, docID := range sortedKeys(eventDocs) {
		var ev state.LogEvent
		if err := docstore.Decode(eventDocs[docID], &ev); err != nil {
			vs = append(vs, Violation{
				Invariant: "event_log.decode",
				UserID:    userID,
				EntityID:  docID,
				Message:   fmt.Sprintf("failed to decode event document: %v", err),
			})
			continue
		}
		expectedID := fmt.Sprintf("%020d", ev.Seq)
		if docID != expectedID {
			vs = append(vs, Violation{
				Invariant: "event_log.doc_id_mismatch",
				UserID:    userID,
				EntityID:  docID,
				Message:   fmt.Sprintf("event doc ID %q does not match formatted seq %q", docID, expectedID),
			})
		}
		if ev.Seq < 1 || ev.Seq > eventPos {
			vs = append(vs, Violation{
				Invariant: "event_log.seq_out_of_range",
				UserID:    userID,
				EntityID:  docID,
				Message:   fmt.Sprintf("event seq %d is outside 1..%d", ev.Seq, eventPos),
			})
			continue
		}
		if seenSeq[ev.Seq] {
			vs = append(vs, Violation{
				Invariant: "event_log.duplicate_seq",
				UserID:    userID,
				EntityID:  docID,
				Message:   fmt.Sprintf("duplicate event seq %d", ev.Seq),
			})
			continue
		}
		seenSeq[ev.Seq] = true
	}

	for seq := int64(1); seq <= eventPos; seq++ {
		if !seenSeq[seq] {
			vs = append(vs, Violation{
				Invariant: "event_log.gap",
				UserID:    userID,
				EntityID:  fmt.Sprintf("%020d", seq),
				Message:   fmt.Sprintf("missing event seq %d in 1..%d", seq, eventPos),
			})
		}
	}

	return eventPos, vs
}

func (c *Checker) checkEventPositionLocked(userID string, pos int64) []Violation {
	var vs []Violation
	if pos < 0 {
		vs = append(vs, Violation{
			Invariant: "event_log.negative_position",
			UserID:    userID,
			Message:   fmt.Sprintf("EventPosition is negative: %d", pos),
		})
		return vs
	}
	if prev, ok := c.eventPositions[userID]; ok && pos < prev {
		vs = append(vs, Violation{
			Invariant: "event_log.position_monotonic",
			UserID:    userID,
			Message:   fmt.Sprintf("EventPosition decreased from %d to %d", prev, pos),
		})
		return vs
	}
	c.eventPositions[userID] = pos
	return vs
}

func (c *Checker) checkReadMarkLocked(userID string, mark state.ReadMark, eventPos int64) []Violation {
	var vs []Violation
	if mark.Position < 0 {
		vs = append(vs, Violation{
			Invariant: "readmark.negative_position",
			UserID:    userID,
			Message:   fmt.Sprintf("ReadMark.Position is negative: %d", mark.Position),
		})
	}
	if mark.Position > 0 && eventPos >= 0 && mark.Position > eventPos {
		vs = append(vs, Violation{
			Invariant: "readmark.beyond_event_head",
			UserID:    userID,
			Message:   fmt.Sprintf("ReadMark.Position (%d) exceeds EventPosition (%d)", mark.Position, eventPos),
		})
	}
	if prev, ok := c.readMarks[userID]; ok {
		if mark.Through.Before(prev.Through) {
			vs = append(vs, Violation{
				Invariant: "readmark.through_monotonic",
				UserID:    userID,
				EntityID:  mark.MessageID,
				Message:   fmt.Sprintf("ReadMark.Through moved backwards from %s to %s", prev.Through.Format(time.RFC3339Nano), mark.Through.Format(time.RFC3339Nano)),
			})
		}
		if mark.Position < prev.Position {
			vs = append(vs, Violation{
				Invariant: "readmark.position_monotonic",
				UserID:    userID,
				EntityID:  mark.MessageID,
				Message:   fmt.Sprintf("ReadMark.Position decreased from %d to %d", prev.Position, mark.Position),
			})
		}
		if mark.Position == prev.Position && prev.MessageID != "" && mark.MessageID != prev.MessageID {
			vs = append(vs, Violation{
				Invariant: "readmark.message_changed_without_advance",
				UserID:    userID,
				EntityID:  mark.MessageID,
				Message:   fmt.Sprintf("ReadMark.MessageID changed from %q to %q at same Position %d", prev.MessageID, mark.MessageID, mark.Position),
			})
		}
	}
	if len(vs) == 0 {
		c.readMarks[userID] = mark
	}
	return vs
}

func (c *Checker) checkConversationLocked(userID string, ctrl state.ConversationControl) []Violation {
	var vs []Violation
	if ctrl.Received < 0 || ctrl.Consumed < 0 || ctrl.UserReceived < 0 || ctrl.UserRouted < 0 || ctrl.Fence < 0 {
		vs = append(vs, Violation{
			Invariant: "conversation.non_negative",
			UserID:    userID,
			Message:   fmt.Sprintf("negative conversation counter: received=%d consumed=%d user_received=%d user_routed=%d fence=%d", ctrl.Received, ctrl.Consumed, ctrl.UserReceived, ctrl.UserRouted, ctrl.Fence),
		})
	}
	if ctrl.Consumed > ctrl.Received {
		vs = append(vs, Violation{
			Invariant: "conversation.consumed_within_received",
			UserID:    userID,
			Message:   fmt.Sprintf("Consumed (%d) exceeds Received (%d)", ctrl.Consumed, ctrl.Received),
		})
	}
	if ctrl.UserReceived > ctrl.Received {
		vs = append(vs, Violation{
			Invariant: "conversation.user_received_within_received",
			UserID:    userID,
			Message:   fmt.Sprintf("UserReceived (%d) exceeds Received (%d)", ctrl.UserReceived, ctrl.Received),
		})
	}
	if ctrl.UserRouted > ctrl.UserReceived {
		vs = append(vs, Violation{
			Invariant: "conversation.user_routed_within_user_received",
			UserID:    userID,
			Message:   fmt.Sprintf("UserRouted (%d) exceeds UserReceived (%d)", ctrl.UserRouted, ctrl.UserReceived),
		})
	}
	if prev, ok := c.conversations[userID]; ok {
		if ctrl.Received < prev.received {
			vs = append(vs, Violation{
				Invariant: "conversation.received_monotonic",
				UserID:    userID,
				Message:   fmt.Sprintf("Received decreased from %d to %d", prev.received, ctrl.Received),
			})
		}
		if ctrl.Consumed < prev.consumed {
			vs = append(vs, Violation{
				Invariant: "conversation.consumed_monotonic",
				UserID:    userID,
				Message:   fmt.Sprintf("Consumed decreased from %d to %d", prev.consumed, ctrl.Consumed),
			})
		}
		if ctrl.UserReceived < prev.userReceived {
			vs = append(vs, Violation{
				Invariant: "conversation.user_received_monotonic",
				UserID:    userID,
				Message:   fmt.Sprintf("UserReceived decreased from %d to %d", prev.userReceived, ctrl.UserReceived),
			})
		}
		if ctrl.UserRouted < prev.userRouted {
			vs = append(vs, Violation{
				Invariant: "conversation.user_routed_monotonic",
				UserID:    userID,
				Message:   fmt.Sprintf("UserRouted decreased from %d to %d", prev.userRouted, ctrl.UserRouted),
			})
		}
		if ctrl.Fence < prev.fence {
			vs = append(vs, Violation{
				Invariant: "conversation.fence_monotonic",
				UserID:    userID,
				Message:   fmt.Sprintf("Fence decreased from %d to %d", prev.fence, ctrl.Fence),
			})
		}
	}
	if len(vs) == 0 {
		c.conversations[userID] = convBaseline{
			received:     ctrl.Received,
			consumed:     ctrl.Consumed,
			userReceived: ctrl.UserReceived,
			userRouted:   ctrl.UserRouted,
			fence:        ctrl.Fence,
		}
	}
	return vs
}

func (c *Checker) checkRunsLocked(userID string, runs map[string]state.Run, completeView bool) []Violation {
	var vs []Violation

	if completeView {
		var missing []string
		for k := range c.runs {
			if k.userID == userID {
				if _, ok := runs[k.entityID]; !ok {
					missing = append(missing, k.entityID)
				}
			}
		}
		slices.Sort(missing)
		for _, id := range missing {
			vs = append(vs, Violation{
				Invariant: "run.durable",
				UserID:    userID,
				EntityID:  id,
				Message:   "previously observed run disappeared from storage",
			})
		}
	}

	for _, runID := range sortedKeys(runs) {
		run := runs[runID]
		key := userEntityKey{userID: userID, entityID: runID}

		if !isValidRunStatus(run.Status) {
			vs = append(vs, Violation{
				Invariant: "run.valid_status",
				UserID:    userID,
				EntityID:  runID,
				Message:   fmt.Sprintf("invalid run status %q", run.Status),
			})
		}

		if run.Revision < 0 || run.Position < 0 || run.Incorporated < 0 {
			vs = append(vs, Violation{
				Invariant: "run.counters_non_negative",
				UserID:    userID,
				EntityID:  runID,
				Message:   fmt.Sprintf("negative run counters: revision=%d position=%d incorporated=%d", run.Revision, run.Position, run.Incorporated),
			})
		}
		if run.Incorporated > state.TaskPosition(run) {
			vs = append(vs, Violation{
				Invariant: "run.incorporated_within_position",
				UserID:    userID,
				EntityID:  runID,
				Message:   fmt.Sprintf("Incorporated (%d) exceeds TaskPosition (%d)", run.Incorporated, state.TaskPosition(run)),
			})
		}

		vs = append(vs, checkMoney(userID, runID, "run.budget_valid", "Budget", run.Budget)...)
		vs = append(vs, checkMoney(userID, runID, "run.spent_valid", "Spent", run.Spent)...)
		vs = append(vs, checkMoney(userID, runID, "run.subtask_spent_valid", "SubtaskSpent", run.SubtaskSpent)...)

		isTerminal := run.Status != state.RunRunning && isValidRunStatus(run.Status)
		if isTerminal {
			if run.Finishing != nil {
				vs = append(vs, Violation{
					Invariant: "run.terminal_clears_finishing",
					UserID:    userID,
					EntityID:  runID,
					Message:   fmt.Sprintf("terminal run (%s) still has non-nil Finishing", run.Status),
				})
			}
			if run.FinishedAt == nil {
				vs = append(vs, Violation{
					Invariant: "run.terminal_has_finished_at",
					UserID:    userID,
					EntityID:  runID,
					Message:   fmt.Sprintf("terminal run (%s) has nil FinishedAt", run.Status),
				})
			}
			if run.Question != nil {
				vs = append(vs, Violation{
					Invariant: "run.terminal_clears_question",
					UserID:    userID,
					EntityID:  runID,
					Message:   fmt.Sprintf("terminal run (%s) still has an open Question (%s)", run.Status, run.Question.ID),
				})
			}
		}

		if prev, seen := c.runs[key]; seen {
			if run.Revision < prev.revision {
				vs = append(vs, Violation{
					Invariant: "run.revision_monotonic",
					UserID:    userID,
					EntityID:  runID,
					Message:   fmt.Sprintf("Run.Revision decreased from %d to %d", prev.revision, run.Revision),
				})
			}
			if run.Position < prev.position {
				vs = append(vs, Violation{
					Invariant: "run.position_monotonic",
					UserID:    userID,
					EntityID:  runID,
					Message:   fmt.Sprintf("Run.Position decreased from %d to %d", prev.position, run.Position),
				})
			}
			if run.Incorporated < prev.incorporated {
				vs = append(vs, Violation{
					Invariant: "run.incorporated_monotonic",
					UserID:    userID,
					EntityID:  runID,
					Message:   fmt.Sprintf("Run.Incorporated decreased from %d to %d", prev.incorporated, run.Incorporated),
				})
			}
			if !math.IsNaN(run.Spent) && run.Spent < prev.spent {
				vs = append(vs, Violation{
					Invariant: "run.spent_monotonic",
					UserID:    userID,
					EntityID:  runID,
					Message:   fmt.Sprintf("Run.Spent decreased from %.6f to %.6f", prev.spent, run.Spent),
				})
			}
			if !math.IsNaN(run.SubtaskSpent) && run.SubtaskSpent < prev.subtaskSpent {
				vs = append(vs, Violation{
					Invariant: "run.subtask_spent_monotonic",
					UserID:    userID,
					EntityID:  runID,
					Message:   fmt.Sprintf("Run.SubtaskSpent decreased from %.6f to %.6f", prev.subtaskSpent, run.SubtaskSpent),
				})
			}
			if prev.terminal != "" && run.Status != prev.terminal {
				vs = append(vs, Violation{
					Invariant: "run.terminal_status_immutable",
					UserID:    userID,
					EntityID:  runID,
					Message:   fmt.Sprintf("terminal run status changed from %q to %q", prev.terminal, run.Status),
				})
			}
		}

		next := c.runs[key]
		if run.Revision > next.revision {
			next.revision = run.Revision
		}
		if run.Position > next.position {
			next.position = run.Position
		}
		if run.Incorporated > next.incorporated {
			next.incorporated = run.Incorporated
		}
		if !math.IsNaN(run.Spent) && !math.IsInf(run.Spent, 0) && run.Spent > next.spent {
			next.spent = run.Spent
		}
		if !math.IsNaN(run.SubtaskSpent) && !math.IsInf(run.SubtaskSpent, 0) && run.SubtaskSpent > next.subtaskSpent {
			next.subtaskSpent = run.SubtaskSpent
		}
		if next.terminal == "" && isTerminal {
			next.terminal = run.Status
		}
		c.runs[key] = next

		if run.ParentID != "" {
			if run.ParentID == runID {
				vs = append(vs, Violation{
					Invariant: "subtask.no_self_parent",
					UserID:    userID,
					EntityID:  runID,
					Message:   "subtask ParentID points to itself",
				})
			}
			vs = append(vs, c.checkOwnedByLocked(c.runOwners, "run", run.ParentID, userID, "subtask "+runID+" ParentID")...)
			parent, exists := runs[run.ParentID]
			if !exists {
				if completeView {
					vs = append(vs, Violation{
						Invariant: "subtask.parent_exists",
						UserID:    userID,
						EntityID:  runID,
						Message:   fmt.Sprintf("subtask ParentID %q does not exist for user", run.ParentID),
					})
				}
			} else {
				if parent.ParentID != "" {
					vs = append(vs, Violation{
						Invariant: "subtask.max_depth_one",
						UserID:    userID,
						EntityID:  runID,
						Message:   fmt.Sprintf("subtask parent %q itself has ParentID %q (nesting > 1 level)", parent.ID, parent.ParentID),
					})
				}
				linked := false
				for _, link := range parent.Subtasks {
					if link.ID == runID {
						linked = true
						break
					}
				}
				if !linked {
					vs = append(vs, Violation{
						Invariant: "subtask.linked_on_parent",
						UserID:    userID,
						EntityID:  runID,
						Message:   fmt.Sprintf("parent %q does not list subtask %q in Subtasks", parent.ID, runID),
					})
				}
			}
		}

		for _, link := range run.Subtasks {
			vs = append(vs, c.checkOwnedByLocked(c.runOwners, "run", link.ID, userID, "run "+runID+" SubtaskLink")...)
			child, exists := runs[link.ID]
			if !exists {
				if completeView {
					vs = append(vs, Violation{
						Invariant: "subtask.child_exists",
						UserID:    userID,
						EntityID:  runID,
						Message:   fmt.Sprintf("Subtasks link %q does not exist for user", link.ID),
					})
				}
				continue
			}
			if child.ParentID != runID {
				vs = append(vs, Violation{
					Invariant: "subtask.child_parent_matches",
					UserID:    userID,
					EntityID:  link.ID,
					Message:   fmt.Sprintf("linked child ParentID is %q, expected %q", child.ParentID, runID),
				})
			}
			if link.Status != child.Status {
				vs = append(vs, Violation{
					Invariant: "subtask.link_status_matches",
					UserID:    userID,
					EntityID:  link.ID,
					Message:   fmt.Sprintf("parent %q records SubtaskLink status %q, but child status is %q", runID, link.Status, child.Status),
				})
			}
			if isTerminal && run.Finishing == nil {
				if child.Status == state.RunRunning {
					vs = append(vs, Violation{
						Invariant: "subtask.terminal_parent_has_running_child",
						UserID:    userID,
						EntityID:  runID,
						Message:   fmt.Sprintf("terminal parent (%s) still has running subtask %q", run.Status, child.ID),
					})
				}
				if child.CleanupPending {
					vs = append(vs, Violation{
						Invariant: "subtask.terminal_parent_has_cleanup_pending_child",
						UserID:    userID,
						EntityID:  runID,
						Message:   fmt.Sprintf("terminal parent (%s) has subtask %q with CleanupPending=true", run.Status, child.ID),
					})
				}
			}
		}

		if completeView && len(run.Subtasks) > 0 {
			var childSpentSum float64
			anyChildUnknown := false
			allChildrenPresent := true
			for _, link := range run.Subtasks {
				child, exists := runs[link.ID]
				if !exists {
					allChildrenPresent = false
					break
				}
				childSpentSum += child.Spent
				if child.CostUnknown {
					anyChildUnknown = true
				}
			}
			if allChildrenPresent {
				if math.Abs(run.SubtaskSpent-childSpentSum) > 1e-6 {
					vs = append(vs, Violation{
						Invariant: "subtask.spent_rollup_consistent",
						UserID:    userID,
						EntityID:  runID,
						Message:   fmt.Sprintf("parent SubtaskSpent=%.9f diverges from sum(child.Spent)=%.9f", run.SubtaskSpent, childSpentSum),
					})
				}
				if anyChildUnknown && !run.CostUnknown {
					vs = append(vs, Violation{
						Invariant: "subtask.cost_unknown_rollup_consistent",
						UserID:    userID,
						EntityID:  runID,
						Message:   "child has CostUnknown=true but parent CostUnknown=false",
					})
				}
			}
		}

		if run.Origin != nil {
			if run.Origin.ContinuesTaskID != "" {
				vs = append(vs, c.checkOwnedByLocked(c.runOwners, "run", run.Origin.ContinuesTaskID, userID, "run "+runID+" Origin.ContinuesTaskID")...)
			}
			if run.Origin.AutomationID != "" {
				vs = append(vs, c.checkOwnedByLocked(c.automationOwners, "automation", run.Origin.AutomationID, userID, "run "+runID+" Origin.AutomationID")...)
			}
			if run.Origin.OccurrenceID != "" {
				vs = append(vs, c.checkOwnedByLocked(c.occurrenceOwners, "occurrence", run.Origin.OccurrenceID, userID, "run "+runID+" Origin.OccurrenceID")...)
			}
		}
	}

	return vs
}

func (c *Checker) checkOperationsLocked(userID string, runs map[string]state.Run, opsByRun map[string][]rawOperationRecord, completeView bool) []Violation {
	var vs []Violation

	present := map[userRunOpKey]bool{}
	for _, runID := range sortedKeys(opsByRun) {
		if completeView {
			if _, ok := runs[runID]; !ok {
				vs = append(vs, Violation{
					Invariant: "operation.run_exists",
					UserID:    userID,
					EntityID:  runID,
					Message:   "operations exist for a run that does not exist",
				})
			}
		}
		for _, rec := range opsByRun[runID] {
			key := userRunOpKey{userID: userID, runID: runID, opID: rec.docID}
			present[key] = true
			op := rec.op
			entityID := runID + "/" + rec.docID

			if op.ID != "" && op.ID != rec.docID {
				vs = append(vs, Violation{
					Invariant: "operation.id_matches_doc",
					UserID:    userID,
					EntityID:  entityID,
					Message:   fmt.Sprintf("operation doc ID %q does not match OperationRecord.ID %q", rec.docID, op.ID),
				})
			}

			if !rec.evidenceRecorded {
				if prev, seen := c.operations[key]; seen && prev.evidenceRecorded {
					vs = append(vs, Violation{
						Invariant: "operation.evidence_durable",
						UserID:    userID,
						EntityID:  entityID,
						Message:   fmt.Sprintf("recorded operation evidence (outcome=%q) reverted to unrecorded admission", prev.outcome),
					})
				}
				continue
			}

			if !isValidOpOutcome(op.Outcome) {
				vs = append(vs, Violation{
					Invariant: "operation.valid_outcome",
					UserID:    userID,
					EntityID:  entityID,
					Message:   fmt.Sprintf("invalid operation outcome %q", op.Outcome),
				})
				continue
			}

			actionDigest := ""
			if op.Action != nil {
				actionDigest = op.Action.Digest
			}

			hadOpViolation := false
			if prev, seen := c.operations[key]; seen && prev.evidenceRecorded {
				beforeCount := len(vs)
				if prev.id != op.ID || prev.callID != op.CallID || prev.name != op.Name ||
					prev.effectClass != op.EffectClass || prev.revision != op.Revision {
					vs = append(vs, Violation{
						Invariant: "operation.identity_immutable",
						UserID:    userID,
						EntityID:  entityID,
						Message: fmt.Sprintf(
							"operation identity mutated: prior=(call=%s name=%s effect=%s rev=%d) got=(call=%s name=%s effect=%s rev=%d)",
							prev.callID, prev.name, prev.effectClass, prev.revision,
							op.CallID, op.Name, op.EffectClass, op.Revision,
						),
					})
				}
				if op.Attempt < prev.attempt || (prev.outcome != "pending" && prev.outcome != "ready" && op.Attempt != prev.attempt) {
					vs = append(vs, Violation{
						Invariant: "operation.attempt_immutable",
						UserID:    userID,
						EntityID:  entityID,
						Message:   fmt.Sprintf("operation attempt changed illegally from %d to %d (prior outcome=%q)", prev.attempt, op.Attempt, prev.outcome),
					})
				}
				if prev.actionDigest != "" && actionDigest != prev.actionDigest {
					vs = append(vs, Violation{
						Invariant: "operation.action_digest_immutable",
						UserID:    userID,
						EntityID:  entityID,
						Message:   fmt.Sprintf("operation Action.Digest changed from %q to %q", prev.actionDigest, actionDigest),
					})
				}
				if prev.cleared != "" && op.Cleared != prev.cleared {
					vs = append(vs, Violation{
						Invariant: "operation.cleared_immutable",
						UserID:    userID,
						EntityID:  entityID,
						Message:   fmt.Sprintf("operation Cleared changed from %q to %q", prev.cleared, op.Cleared),
					})
				}

				if isTerminalOpOutcome(prev.outcome) {
					if op.Outcome != prev.outcome || op.ResultRef != prev.resultRef {
						vs = append(vs, Violation{
							Invariant: "operation.single_settlement",
							UserID:    userID,
							EntityID:  entityID,
							Message: fmt.Sprintf(
								"settled operation changed: prior=(outcome=%q result_ref=%q) got=(outcome=%q result_ref=%q)",
								prev.outcome, prev.resultRef, op.Outcome, op.ResultRef,
							),
						})
					}
				} else if op.Outcome != prev.outcome {
					if !opOutcomeMayFollow(prev.outcome, op.Outcome) {
						vs = append(vs, Violation{
							Invariant: "operation.outcome_transition",
							UserID:    userID,
							EntityID:  entityID,
							Message:   fmt.Sprintf("illegal operation outcome transition %q -> %q", prev.outcome, op.Outcome),
						})
					}
				} else if prev.outcome != "pending" && op.ResultRef != prev.resultRef {
					vs = append(vs, Violation{
						Invariant: "operation.result_ref_immutable",
						UserID:    userID,
						EntityID:  entityID,
						Message:   fmt.Sprintf("operation ResultRef changed from %q to %q without outcome transition (outcome=%q)", prev.resultRef, op.ResultRef, op.Outcome),
					})
				}
				hadOpViolation = len(vs) > beforeCount
			}

			if !hadOpViolation {
				c.operations[key] = opBaseline{
					evidenceRecorded: true,
					id:               op.ID,
					callID:           op.CallID,
					name:             op.Name,
					effectClass:      op.EffectClass,
					outcome:          op.Outcome,
					resultRef:        op.ResultRef,
					revision:         op.Revision,
					attempt:          op.Attempt,
					cleared:          op.Cleared,
					actionDigest:     actionDigest,
				}
			}
		}
	}

	if completeView {
		var missing []userRunOpKey
		for k, base := range c.operations {
			if k.userID == userID && base.evidenceRecorded && !present[k] {
				missing = append(missing, k)
			}
		}
		slices.SortFunc(missing, func(a, b userRunOpKey) int {
			if a.runID != b.runID {
				return strings.Compare(a.runID, b.runID)
			}
			return strings.Compare(a.opID, b.opID)
		})
		for _, k := range missing {
			vs = append(vs, Violation{
				Invariant: "operation.durable",
				UserID:    userID,
				EntityID:  k.runID + "/" + k.opID,
				Message:   "previously settled/recorded operation disappeared from storage",
			})
		}
	}

	return vs
}

func (c *Checker) checkCommandsLocked(userID string, runs map[string]state.Run, cmds []state.Command) []Violation {
	var vs []Violation
	for _, cmd := range cmds {
		vs = append(vs, c.checkOwnedByLocked(c.runOwners, "run", cmd.RunID, userID, "command "+cmd.ID)...)
		if cmd.RunID != "" {
			if _, ok := runs[cmd.RunID]; !ok && c.backend != nil {
				vs = append(vs, Violation{
					Invariant: "command.run_exists",
					UserID:    userID,
					EntityID:  cmd.ID,
					Message:   fmt.Sprintf("command references missing run %q", cmd.RunID),
				})
			}
		}
		if v, ok := c.recordCommandReceiptLocked(userID, state.CommandReceipt{Command: cmd}, true); !ok {
			vs = append(vs, v)
		}
	}
	return vs
}

func (c *Checker) checkInteractionsLocked(
	ctx context.Context,
	userID string,
	runs map[string]state.Run,
	interactions []rawInteractionRecord,
	permSvc *permissions.Service,
	now time.Time,
) []Violation {
	var vs []Violation
	for _, rec := range interactions {
		r := rec.resource
		raw := rec.interaction

		if raw.TaskID != "" {
			vs = append(vs, c.checkOwnedByLocked(c.runOwners, "run", raw.TaskID, userID, "interaction "+r.ID)...)
		}
		if raw.AutomationID != "" {
			vs = append(vs, c.checkOwnedByLocked(c.automationOwners, "automation", raw.AutomationID, userID, "interaction "+r.ID)...)
		}

		if raw.Status == "pending" && r.Index["status"] != "pending" {
			vs = append(vs, Violation{
				Invariant: "interaction.index_status_consistent",
				UserID:    userID,
				EntityID:  r.ID,
				Message:   fmt.Sprintf("stored interaction status is %q but index.status is %q", raw.Status, r.Index["status"]),
			})
		}
		if raw.TaskID != "" && r.Index["task_id"] != raw.TaskID {
			vs = append(vs, Violation{
				Invariant: "interaction.index_task_id_consistent",
				UserID:    userID,
				EntityID:  r.ID,
				Message:   fmt.Sprintf("stored interaction task_id is %q but index.task_id is %q", raw.TaskID, r.Index["task_id"]),
			})
		}

		if raw.ExpiresAt != nil && raw.Resolution != nil && raw.Resolution.Kind == "approve" &&
			raw.ResolvedAt != nil && raw.ResolvedAt.After(*raw.ExpiresAt) {
			vs = append(vs, Violation{
				Invariant: "interaction.expired_approved",
				UserID:    userID,
				EntityID:  r.ID,
				Message:   fmt.Sprintf("interaction was approved at %s after expiring at %s", raw.ResolvedAt.Format(time.RFC3339Nano), raw.ExpiresAt.Format(time.RFC3339Nano)),
			})
		}

		effective := raw
		if permSvc != nil {
			got, err := permSvc.Interaction(ctx, r.ID)
			if err != nil {
				vs = append(vs, Violation{
					Invariant: "interaction.service_read",
					UserID:    userID,
					EntityID:  r.ID,
					Message:   fmt.Sprintf("permissions.Service.Interaction failed: %v", err),
				})
				continue
			}
			effective = got
		} else if raw.Status == "pending" && raw.ExpiresAt != nil && now.After(*raw.ExpiresAt) {
			effective.Status = "expired"
		}

		if raw.Status == "pending" && raw.ExpiresAt != nil && now.After(*raw.ExpiresAt) {
			if effective.Status != "expired" {
				vs = append(vs, Violation{
					Invariant: "interaction.expired_status",
					UserID:    userID,
					EntityID:  r.ID,
					Message:   fmt.Sprintf("interaction expired at %s (now=%s) but permissions.Service reported status %q", raw.ExpiresAt.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), effective.Status),
				})
			}
			continue
		}

		if effective.Status != "pending" || effective.TaskID == "" {
			continue
		}

		run, exists := runs[effective.TaskID]
		if !exists {
			if c.backend != nil {
				vs = append(vs, Violation{
					Invariant: "interaction.task_exists",
					UserID:    userID,
					EntityID:  r.ID,
					Message:   fmt.Sprintf("pending interaction references missing task %q", effective.TaskID),
				})
			}
			continue
		}

		if effective.InstructionRevision != nil && *effective.InstructionRevision != run.Revision {
			vs = append(vs, Violation{
				Invariant: "interaction.stale_revision",
				UserID:    userID,
				EntityID:  r.ID,
				Message:   fmt.Sprintf("pending interaction on run %q has InstructionRevision=%d, but run is at Revision=%d (status=%s)", run.ID, *effective.InstructionRevision, run.Revision, run.Status),
			})
		}

		if run.Status == state.RunCancelled && run.CancelRequested && run.ParentID == "" {
			vs = append(vs, Violation{
				Invariant: "interaction.pending_on_cancelled_run",
				UserID:    userID,
				EntityID:  r.ID,
				Message:   fmt.Sprintf("interaction remains pending on cancelled top-level run %q", run.ID),
			})
		}
	}
	return vs
}

func (c *Checker) checkAutomationsAndOccurrencesLocked(
	userID string,
	automations map[string]storedAutomation,
	occurrences []storedOccurrence,
) []Violation {
	var vs []Violation

	for _, autoID := range sortedKeys(automations) {
		a := automations[autoID]
		vs = append(vs, checkMoney(userID, autoID, "automation.budget_valid", "BudgetUSD", a.BudgetUSD)...)
		if a.LastOccurrenceID != "" {
			vs = append(vs, c.checkOwnedByLocked(c.occurrenceOwners, "occurrence", a.LastOccurrenceID, userID, "automation "+autoID+" LastOccurrenceID")...)
		}

		key := userEntityKey{userID: userID, entityID: autoID}
		if prev, seen := c.automations[key]; seen {
			if a.Generation < prev.generation {
				vs = append(vs, Violation{
					Invariant: "automation.generation_monotonic",
					UserID:    userID,
					EntityID:  autoID,
					Message:   fmt.Sprintf("automation Generation decreased from %d to %d", prev.generation, a.Generation),
				})
			}
			if prev.cursor != nil && (a.Cursor == nil || a.Cursor.Before(*prev.cursor)) {
				got := "<nil>"
				if a.Cursor != nil {
					got = a.Cursor.Format(time.RFC3339Nano)
				}
				vs = append(vs, Violation{
					Invariant: "automation.cursor_monotonic",
					UserID:    userID,
					EntityID:  autoID,
					Message:   fmt.Sprintf("automation Cursor moved backwards from %s to %s", prev.cursor.Format(time.RFC3339Nano), got),
				})
			}
		}
		var cursorCopy *time.Time
		if a.Cursor != nil {
			t := a.Cursor.UTC()
			cursorCopy = &t
		}
		if prev, seen := c.automations[key]; !seen || a.Generation >= prev.generation {
			nextBase := automationBaseline{generation: a.Generation, cursor: cursorCopy}
			if seen && prev.cursor != nil && (cursorCopy == nil || cursorCopy.Before(*prev.cursor)) {
				nextBase.cursor = prev.cursor
			}
			c.automations[key] = nextBase
		}
	}

	vs = append(vs, c.checkOccurrenceUniquenessAndOwnershipLocked(userID, automations, occurrences)...)
	return vs
}

func (c *Checker) checkOccurrenceUniquenessAndOwnershipLocked(
	userID string,
	automations map[string]storedAutomation,
	occurrences []storedOccurrence,
) []Violation {
	var vs []Violation
	seenSlotKey := map[string]string{}
	seenTaskID := map[string]string{}

	for _, occ := range occurrences {
		vs = append(vs, c.checkOwnedByLocked(c.automationOwners, "automation", occ.AutomationID, userID, "occurrence "+occ.ID)...)
		if _, exists := automations[occ.AutomationID]; !exists {
			vs = append(vs, Violation{
				Invariant: "occurrence.automation_exists",
				UserID:    userID,
				EntityID:  occ.ID,
				Message:   fmt.Sprintf("occurrence references missing automation %q", occ.AutomationID),
			})
		}

		sourceKind := "scheduled"
		if occ.ID == expectedOccurrenceID(occ.AutomationID, occ.Generation, occ.ScheduledAt, "run_now") {
			sourceKind = "run_now"
		}
		slotKey := fmt.Sprintf("%s|%d|%s|%s", occ.AutomationID, occ.Generation, occ.ScheduledAt.UTC().Format(time.RFC3339), sourceKind)
		if firstID, dup := seenSlotKey[slotKey]; dup && firstID != occ.ID {
			vs = append(vs, Violation{
				Invariant: "scheduler.duplicate_occurrence_key",
				UserID:    userID,
				EntityID:  occ.ID,
				Message:   fmt.Sprintf("duplicate occurrence for slot key %q (already present as %q)", slotKey, firstID),
			})
		} else {
			seenSlotKey[slotKey] = occ.ID
		}

		for _, taskID := range occ.TaskIDs {
			vs = append(vs, c.checkOwnedByLocked(c.runOwners, "run", taskID, userID, "occurrence "+occ.ID+" TaskID")...)
			if firstOcc, dup := seenTaskID[taskID]; dup && firstOcc != occ.ID {
				vs = append(vs, Violation{
					Invariant: "scheduler.duplicate_occurrence_task",
					UserID:    userID,
					EntityID:  occ.ID,
					Message:   fmt.Sprintf("task ID %q is linked to multiple occurrences (%q and %q)", taskID, firstOcc, occ.ID),
				})
			} else {
				seenTaskID[taskID] = occ.ID
			}
		}
	}

	return vs
}

func (c *Checker) registerOwnerLocked(owners map[string]string, kind, entityID, userID string) []Violation {
	if entityID == "" {
		return nil
	}
	if prev, exists := owners[entityID]; exists && prev != userID {
		return []Violation{{
			Invariant: "isolation.cross_user_entity",
			UserID:    userID,
			EntityID:  entityID,
			Message:   fmt.Sprintf("%s ID %q belongs to user %q but also appeared under user %q", kind, entityID, prev, userID),
		}}
	}
	owners[entityID] = userID
	return nil
}

func (c *Checker) checkOwnedByLocked(owners map[string]string, kind, entityID, userID, source string) []Violation {
	if entityID == "" {
		return nil
	}
	if owner, exists := owners[entityID]; exists && owner != userID {
		return []Violation{{
			Invariant: "isolation.cross_user_reference",
			UserID:    userID,
			EntityID:  entityID,
			Message:   fmt.Sprintf("%s references %s ID %q owned by user %q", source, kind, entityID, owner),
		}}
	}
	return nil
}

func checkMoney(userID, entityID, invariant, field string, val float64) []Violation {
	if math.IsNaN(val) || math.IsInf(val, 0) || val < 0 {
		return []Violation{{
			Invariant: invariant,
			UserID:    userID,
			EntityID:  entityID,
			Message:   fmt.Sprintf("%s is invalid: %v", field, val),
		}}
	}
	return nil
}

func isValidRunStatus(s state.RunStatus) bool {
	switch s {
	case state.RunRunning, state.RunCompleted, state.RunFailed, state.RunCancelled:
		return true
	default:
		return false
	}
}

func isValidOpOutcome(outcome string) bool {
	switch outcome {
	case "pending", "confirmed", "failed", "uncertain", "skipped", "ready", "attempted", "submitted", "voided":
		return true
	default:
		return false
	}
}

func isTerminalOpOutcome(outcome string) bool {
	switch outcome {
	case "confirmed", "failed", "skipped", "voided":
		return true
	default:
		return false
	}
}

func opOutcomeMayFollow(old, next string) bool {
	switch old {
	case "pending":
		return true
	case "ready":
		return next == "attempted" || next == "voided" || next == "failed"
	case "attempted":
		return next == "submitted" || next == "confirmed" || next == "failed" || next == "uncertain"
	case "submitted":
		return next == "confirmed"
	case "uncertain":
		return next == "confirmed" || next == "failed"
	}
	return false
}

func expectedOccurrenceID(automation string, generation int64, slot time.Time, source string) string {
	key := fmt.Sprintf("%s|%d|%s|%s", automation, generation, slot.UTC().Format(time.RFC3339), source)
	return "occ_" + strings.ReplaceAll(uuid.NewSHA1(uuid.NameSpaceOID, []byte(key)).String(), "-", "")[:24]
}

func tagUser(userID string, vs []Violation) []Violation {
	for i := range vs {
		if vs[i].UserID == "" {
			vs[i].UserID = userID
		}
	}
	return vs
}

func decodeTaskControlsFromSnapshot(snap memstore.BackendSnapshot, owner string) (map[string]state.TaskControl, []Violation) {
	col := "owners/" + owner + "/task_controls"
	docs := snap.Collections[col]
	if len(docs) == 0 {
		return nil, nil
	}
	controls := make(map[string]state.TaskControl, len(docs))
	var vs []Violation
	for _, id := range sortedKeys(docs) {
		var tc state.TaskControl
		if err := docstore.Decode(docs[id], &tc); err != nil {
			vs = append(vs, Violation{
				Invariant: "task_control.decode",
				EntityID:  id,
				Message:   fmt.Sprintf("failed to decode task control document: %v", err),
			})
			continue
		}
		if tc.RunID != "" && tc.RunID != id {
			vs = append(vs, Violation{
				Invariant: "task_control.id_matches_doc",
				EntityID:  id,
				Message:   fmt.Sprintf("task control document ID %q does not match TaskControl.RunID %q", id, tc.RunID),
			})
		}
		controls[id] = tc
	}
	return controls, vs
}

func (c *Checker) checkTaskControlsLocked(userID string, controls map[string]state.TaskControl) []Violation {
	if len(controls) == 0 {
		return nil
	}
	var vs []Violation
	if c.taskControls == nil {
		c.taskControls = map[userEntityKey]taskControlBaseline{}
	}
	baselines := c.taskControls

	for _, runID := range sortedKeys(controls) {
		tc := controls[runID]
		vs = append(vs, c.checkOwnedByLocked(c.runOwners, "run", runID, userID, "task_control "+runID)...)

		if tc.Fence < 0 || tc.Models < 0 || tc.Tools < 0 || tc.Checks < 0 || tc.Position < 0 || tc.Revision < 0 {
			vs = append(vs, Violation{
				Invariant: "task_control.counters_non_negative",
				UserID:    userID,
				EntityID:  runID,
				Message:   fmt.Sprintf("negative task control counters: fence=%d models=%d tools=%d checks=%d position=%d revision=%d", tc.Fence, tc.Models, tc.Tools, tc.Checks, tc.Position, tc.Revision),
			})
		}

		key := userEntityKey{userID: userID, entityID: runID}
		if prev, seen := baselines[key]; seen {
			if tc.Fence < prev.fence {
				vs = append(vs, Violation{
					Invariant: "task_control.fence_monotonic",
					UserID:    userID,
					EntityID:  runID,
					Message:   fmt.Sprintf("TaskControl.Fence decreased from %d to %d", prev.fence, tc.Fence),
				})
			}
			if tc.Owner != "" && tc.Fence > 0 {
				if prevOwner, ok := prev.ownersByFence[tc.Fence]; ok && prevOwner != tc.Owner {
					vs = append(vs, Violation{
						Invariant: "task_control.fence_owner_unique",
						UserID:    userID,
						EntityID:  runID,
						Message:   fmt.Sprintf("TaskControl fence %d observed with conflicting owners %q and %q", tc.Fence, prevOwner, tc.Owner),
					})
				}
			}
			if tc.Models < prev.models {
				vs = append(vs, Violation{
					Invariant: "task_control.models_monotonic",
					UserID:    userID,
					EntityID:  runID,
					Message:   fmt.Sprintf("TaskControl.Models decreased from %d to %d", prev.models, tc.Models),
				})
			}
			if tc.Tools < prev.tools {
				vs = append(vs, Violation{
					Invariant: "task_control.tools_monotonic",
					UserID:    userID,
					EntityID:  runID,
					Message:   fmt.Sprintf("TaskControl.Tools decreased from %d to %d", prev.tools, tc.Tools),
				})
			}
			if tc.Checks < prev.checks {
				vs = append(vs, Violation{
					Invariant: "task_control.checks_monotonic",
					UserID:    userID,
					EntityID:  runID,
					Message:   fmt.Sprintf("TaskControl.Checks decreased from %d to %d", prev.checks, tc.Checks),
				})
			}
			if tc.Position < prev.position {
				vs = append(vs, Violation{
					Invariant: "task_control.position_monotonic",
					UserID:    userID,
					EntityID:  runID,
					Message:   fmt.Sprintf("TaskControl.Position decreased from %d to %d", prev.position, tc.Position),
				})
			}
			if tc.Revision < prev.revision {
				vs = append(vs, Violation{
					Invariant: "task_control.revision_monotonic",
					UserID:    userID,
					EntityID:  runID,
					Message:   fmt.Sprintf("TaskControl.Revision decreased from %d to %d", prev.revision, tc.Revision),
				})
			}
		}

		next := baselines[key]
		if next.ownersByFence == nil {
			next.ownersByFence = map[int64]string{}
		}
		if tc.Fence > next.fence {
			next.fence = tc.Fence
		}
		if tc.Owner != "" && tc.Fence > 0 {
			if _, ok := next.ownersByFence[tc.Fence]; !ok {
				next.ownersByFence[tc.Fence] = tc.Owner
			}
		}
		if tc.Models > next.models {
			next.models = tc.Models
		}
		if tc.Tools > next.tools {
			next.tools = tc.Tools
		}
		if tc.Checks > next.checks {
			next.checks = tc.Checks
		}
		if tc.Position > next.position {
			next.position = tc.Position
		}
		if tc.Revision > next.revision {
			next.revision = tc.Revision
		}
		baselines[key] = next
	}

	return vs
}
