package api

import "sync"

// AssistantDelta carries an ephemeral, non-sequenced streaming text snapshot for an
// in-flight action-free outer reply. It never advances the durable event log cursor.
type AssistantDelta struct {
	TurnID string `json:"turn_id"`
	Text   string `json:"text"`
}

// DeltaBroker fans out ephemeral streaming deltas to active SSE connections for one user.
type DeltaBroker struct {
	mu   sync.RWMutex
	subs map[chan AssistantDelta]struct{}
}

func NewDeltaBroker() *DeltaBroker {
	return &DeltaBroker{subs: make(map[chan AssistantDelta]struct{})}
}

func (b *DeltaBroker) Subscribe() (<-chan AssistantDelta, func()) {
	ch := make(chan AssistantDelta, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

func (b *DeltaBroker) Publish(turnID, text string) {
	if b == nil || turnID == "" {
		return
	}
	delta := AssistantDelta{TurnID: turnID, Text: text}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		// Non-blocking send: drop stale intermediate snapshot if a client lags so
		// the latest text always gets through without slowing the model stream.
		select {
		case ch <- delta:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- delta:
			default:
			}
		}
	}
}
