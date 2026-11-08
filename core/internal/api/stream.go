package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nori/internal/state"
)

// Close the stream before the web service's 180-second request limit so the client can resume.
const streamLifetime = 150 * time.Second

type eventStream struct {
	w       http.ResponseWriter
	control *http.ResponseController
}

func prepareEventStream(w http.ResponseWriter, cacheControl string) eventStream {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("X-Accel-Buffering", "no")
	return eventStream{w: w, control: http.NewResponseController(w)}
}

func (e eventStream) send(kind string, seq int64, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	id, event := "", ""
	// A named legacy heartbeat can carry a record update without advancing the cursor.
	if seq > 0 || kind == "" {
		id = fmt.Sprintf("id: %d\n", seq)
	}
	if kind != "" {
		event = "event: " + kind + "\n"
	}
	if _, err = fmt.Fprintf(e.w, "%s%sdata: %s\n\n", id, event, data); err != nil {
		return err
	}
	return e.control.Flush()
}

func (e eventStream) comment(text string) error {
	if _, err := fmt.Fprintf(e.w, ": %s\n\n", text); err != nil {
		return err
	}
	return e.control.Flush()
}

// The native runtime gateway doesn't forward Last-Event-ID. Its clients send ?after instead.
func resumePosition(r *http.Request) (after int64, supplied, valid bool) {
	text := r.Header.Get("Last-Event-ID")
	if text == "" {
		text = r.URL.Query().Get("after")
	}
	if text == "" {
		return 0, false, true
	}
	value, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	return value, true, err == nil && value >= 0
}

func watchEventBatches[T any](ctx context.Context, watch func(context.Context, func([]T) error) error) (<-chan []T, <-chan error) {
	batches := make(chan []T)
	failed := make(chan error, 1)
	go func() {
		failed <- watch(ctx, func(batch []T) error {
			select {
			case batches <- batch:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	return batches, failed
}

type conversationFeed[T any] struct {
	header    conversationHeader
	limit     int64
	heartbeat time.Duration
	comment   string
	repair    func()
	watch     func(context.Context, func([]T) error) error
	sequence  func(T) int64
	project   func(context.Context, T) (conversationEvent, error)
}

func streamConversation[T any](w http.ResponseWriter, r *http.Request, after int64, feed conversationFeed[T]) {
	position := feed.header.Position
	if after > position {
		writeJSON(w, 400, map[string]string{"error": "Event position is ahead of the conversation."})
		return
	}
	stream := prepareEventStream(w, "no-store")
	_ = stream.control.SetWriteDeadline(time.Time{})
	w.WriteHeader(http.StatusOK)
	if stream.control.Flush() != nil {
		return
	}
	if feed.repair != nil {
		feed.repair()
	}
	if position-after > feed.limit {
		_ = stream.send("", position, conversationEvent{Kind: "reset", Seq: position, Conversation: feed.header, Messages: []messageView{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), streamLifetime)
	defer cancel()
	batches, failed := watchEventBatches(ctx, feed.watch)
	heartbeat := time.NewTicker(feed.heartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-failed:
			if err != nil && ctx.Err() == nil {
				log.Printf("conversation event stream ended: %v", err)
			}
			return
		case <-heartbeat.C:
			if stream.comment(feed.comment) != nil {
				return
			}
			if feed.repair != nil {
				feed.repair()
			}
		case batch := <-batches:
			for _, event := range batch {
				seq := feed.sequence(event)
				if seq <= after {
					continue
				}
				value, err := feed.project(ctx, event)
				if errors.Is(err, state.ErrNotFound) {
					value.Missing = true
				} else if err != nil {
					log.Printf("conversation event %d: %v", seq, err)
					return
				}
				if stream.send("", seq, value) != nil {
					return
				}
				after = seq
			}
		}
	}
}
