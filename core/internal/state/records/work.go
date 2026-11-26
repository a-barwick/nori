package records

import (
	"cmp"
	"context"
	"slices"
	"strconv"

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
		return state.FormatWorkVersion(state.WorkVersionAssistant, strconv.FormatInt(c.Received, 10)), nil
	}
	control, err := d.Control(ctx)
	if err != nil {
		return "", err
	}
	runs, err := documents[state.Run](d.runs().Where("status", "==", string(state.RunRunning)).
		Select("id", "revision", "cancel_requested", "finishing").Documents(ctx))
	if err != nil {
		return "", err
	}
	slices.SortFunc(runs, func(a, b state.Run) int { return cmp.Compare(a.ID, b.ID) })
	for _, run := range runs {
		if run.Finishing != nil {
			return state.FormatWorkVersion(state.WorkVersionFinishing, run.ID, strconv.FormatInt(run.Revision, 10)), nil
		}
		if control.Paused && run.CancelRequested {
			return state.FormatWorkVersion(state.WorkVersionStop, run.ID, strconv.FormatInt(run.Revision, 10)), nil
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
		return state.FormatWorkVersion(state.WorkVersionCommand, commands[0].ID), nil
	}
	if !c.RoutingPending() {
		runs, err := d.PublicationTasks(ctx)
		if err != nil {
			return "", err
		}
		if len(runs) > 0 {
			return state.FormatWorkVersion(state.WorkVersionPublication, runs[0].ID, strconv.FormatInt(runs[0].PublicationGeneration, 10)), nil
		}
	}
	cleanup, err := d.CleanupTasks(ctx)
	for _, run := range cleanup {
		if run.Status != state.RunRunning {
			return state.FormatWorkVersion(state.WorkVersionCleanup, run.ID), err
		}
	}
	return "", err
}
