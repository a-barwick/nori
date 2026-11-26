package records

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"nori/internal/state"
)

func (d *Database) WorkVersion(ctx context.Context, tasks bool) (string, error) {
	now := d.now()
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
	runs, err := documents[state.Run](d.runs().Where("status", "==", "running").
		Select("id", "revision", "cancel_requested", "finishing").Documents(ctx))
	if err != nil {
		return "", err
	}
	slices.SortFunc(runs, func(a, b state.Run) int { return cmp.Compare(a.ID, b.ID) })
	for _, run := range runs {
		if run.Finishing != nil {
			return fmt.Sprintf("finishing:%s:%d", run.ID, run.Revision), nil
		}
		if control.Paused && run.CancelRequested {
			return fmt.Sprintf("stop:%s:%d", run.ID, run.Revision), nil
		}
	}
	if control.Paused {
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
