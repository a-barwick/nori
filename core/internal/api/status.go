package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"slices"
	"sync"
	"time"

	"nori/internal/state"
)

// status is the cheap form of session for a polling browser. The browser asks
// it whether the session changed and whether to keep asking, then fetches the
// session only when version differs from the last one it saw.
//
// It reads the conversation control and its active turn, the project control,
// the open tasks and the tasks with pending cleanup: a few documents and two
// small queries, where a session snapshot also reads 80 messages, 30 runs,
// 60 attempts and 100 usage records.
//
// version is an opaque digest of what those reads return, so it changes
// whenever any of these does:
//   - the paused flag
//   - the conversation counters (received, consumed, user routing), the active
//     turn and its lease deadline, whether the turn is still processing, and the
//     active turn's attempt (status, actions, usage, errors, timing)
//   - every field of every open task, as it appears in the session, including a running job's progress line
//   - every task with cleanup pending
//
// That covers new and finished messages, task starts and stops, questions,
// events, artifacts, spend and cancellation. While a turn is processing, the
// lease renewal also changes it, which is more often than the UI needs.
//
// It cannot see changes that touch none of those documents:
//   - a closed task's spend, which can still be projected just after it closes
//   - model usage records written outside an active turn
//   - the session's top-level usage list, which no client displays
//
// The browser stops polling once busy is false, so these show at its next
// refresh rather than live, the same as with polling the session itself.
func (s *server) status(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decode(w, r, &body) {
		return
	}
	reading, err := readStatus(r.Context(), s.Tasks.Store)
	if err != nil {
		writeError(w, err)
		return
	}
	// A polling browser also repairs a wake that was never queued, exactly as
	// the session route does for saved input that nothing is processing.
	if reading.pending && !reading.processing {
		if err := s.wakeConversation(r.Context()); err != nil {
			log.Printf("conversation wake reconciliation failed: %v", err)
			writeJSON(w, 503, map[string]any{"error": "Conversation delivery is unavailable. Retry shortly.", "retryable": true})
			return
		}
	}
	writeJSON(w, 200, statusResponse{Version: reading.version, Busy: reading.busy})
}

type statusResponse struct {
	Version string `json:"version"`
	Busy    bool   `json:"busy"`
}

// statusStore is the part of the store a status poll reads.
type statusStore interface {
	Control(ctx context.Context) (state.Control, error)
	ConversationStatus(ctx context.Context) (state.ConversationControl, *state.ConversationTurn, error)
	OpenTasks(ctx context.Context) ([]state.Run, error)
	CleanupTasks(ctx context.Context) ([]state.Run, error)
}

type statusReading struct {
	version             string
	busy                bool
	pending, processing bool
}

func readStatus(ctx context.Context, store statusStore) (statusReading, error) {
	// Read the conversation first, as a session snapshot does: a task that a
	// finished turn started is then already visible to the open-task read.
	c, turn, err := store.ConversationStatus(ctx)
	if err != nil {
		return statusReading{}, err
	}
	var control state.Control
	var open, cleanup []state.Run
	var failures [3]error
	var reads sync.WaitGroup
	reads.Go(func() { control, failures[0] = store.Control(ctx) })
	reads.Go(func() { open, failures[1] = store.OpenTasks(ctx) })
	reads.Go(func() { cleanup, failures[2] = store.CleanupTasks(ctx) })
	reads.Wait()
	if err := errors.Join(failures[:]...); err != nil {
		return statusReading{}, err
	}
	now := time.Now()
	reading := statusReading{pending: c.Received > c.Consumed, processing: c.Active != "" && c.LeaseUntil.After(now)}
	// busy is the condition under which the web client keeps polling: chat input
	// waiting or processing, or an open task that can still make progress. A
	// paused project only keeps polling for a task that is being stopped.
	reading.busy = reading.pending || reading.processing
	for _, run := range open {
		if run.Status == state.RunRunning && (!control.Paused || run.CancelRequested) {
			reading.busy = true
		}
	}
	if !reading.busy {
		// A task can finish, and queue its result for routing, between the reads
		// above. Look at the conversation once more before telling the client it
		// may stop polling.
		again, _, err := store.ConversationStatus(ctx)
		if err != nil {
			return statusReading{}, err
		}
		reading.busy = again.Received > again.Consumed || (again.Active != "" && again.LeaseUntil.After(time.Now()))
	}
	version, err := digest(control, c, reading.processing, turn, open, cleanup)
	if err != nil {
		return statusReading{}, err
	}
	reading.version = version
	return reading, nil
}

// digest hashes everything the version covers. Whole structs are encoded, so a
// field added later is covered without a change here. The turn's transcripts are
// left out: they are large, appear in no response, and change with the attempt.
func digest(control state.Control, c state.ConversationControl, processing bool, turn *state.ConversationTurn,
	open, cleanup []state.Run) (string, error) {
	if turn != nil {
		trimmed := *turn
		trimmed.Protocol, trimmed.Context = nil, nil
		turn = &trimmed
	}
	byID := func(a, b state.Run) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	}
	slices.SortFunc(open, byID)
	slices.SortFunc(cleanup, byID)
	encoded, err := json.Marshal(struct {
		Control      state.Control
		Conversation state.ConversationControl
		Processing   bool
		Turn         *state.ConversationTurn
		Open         []state.Run
		Cleanup      []state.Run
	}{control, c, processing, turn, open, cleanup})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:16]), nil
}

var _ statusStore = state.ConversationStore(nil)
