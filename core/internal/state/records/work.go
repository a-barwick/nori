package records

import (
	"context"
	"fmt"
	"time"

	"nori/internal/state"
)

// WorkVersion derives API work from its existing durable owners. This is a
// recovery query, not a second journal or a UI snapshot. A live conversation
// lease already owns its wake; a saved command or held result does not.
func (d *Database) WorkVersion(ctx context.Context, tasks bool) (string, error) {
	now := time.Now()
	chats, err := documents[state.Chat](d.chats().Where("attention", "==", true).
		Select("id", "active_turn", "lease_until").Documents(ctx))
	if err != nil {
		return "", err
	}
	for _, chat := range chats {
		if chat.ActiveTurn != nil && !chat.LeaseUntil.After(now) {
			return "chat:" + chat.ID + ":" + *chat.ActiveTurn, nil
		}
	}
	if !tasks {
		return "", nil
	}
	c, _, err := d.ConversationStatus(ctx)
	if err != nil {
		return "", err
	}
	if c.Pending() && !c.Processing(now) {
		return fmt.Sprintf("assistant:%d", c.Received), nil
	}
	control, err := d.Control(ctx)
	if err != nil {
		return "", err
	}
	if control.Paused {
		// Paused commands stay saved for resume. Only accepted Stops need
		// API cleanup while the task workers are unavailable.
		runs, err := d.OpenTasks(ctx)
		if err != nil {
			return "", err
		}
		for _, run := range runs {
			if run.CancelRequested {
				return fmt.Sprintf("stop:%s:%d", run.ID, run.Revision), nil
			}
		}
		return "", nil
	}
	commands, err := documents[state.Command](d.commands().Where("delivered", "==", false).Select("id").Limit(1).Documents(ctx))
	if err != nil {
		return "", err
	}
	if len(commands) > 0 {
		return "command:" + commands[0].ID, nil
	}
	if !c.RoutingPending() {
		runs, err := d.PublicationTasks(ctx)
		if err != nil {
			return "", err
		}
		if len(runs) > 0 {
			return fmt.Sprintf("publication:%s:%d", runs[0].ID, runs[0].PublicationGeneration), nil
		}
	}
	cleanup, err := d.CleanupTasks(ctx)
	for _, run := range cleanup {
		if run.Status != state.RunRunning {
			return "cleanup:" + run.ID, err
		}
	}
	return "", err
}
