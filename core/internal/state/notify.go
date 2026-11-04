package state

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// A notification is how a message reaches Austin when the web app is closed. The service decides whether to interrupt
// him: a message a visible browser already showed sends nothing, quiet hours hold the rest back, and a daily cap bounds
// them. A reminder he set for a time is urgent and goes out at that time.

// Notification statuses. Sending is claimed by one process; seen means a browser showed the message first.
const (
	NotifyPending = "pending"
	NotifySending = "sending"
	NotifySent    = "sent"
	NotifySeen    = "seen"
	NotifySkipped = "skipped"
	NotifyFailed  = "failed"
)

const (
	// NotifyDelay is how long a message waits for an open browser to show it before a notification goes out.
	NotifyDelay = 90 * time.Second
	// NotificationsPerDay bounds the notifications that aren't urgent in one local day.
	NotificationsPerDay = 8
	// NotifyTextBytes bounds the copy of the message a notification carries.
	NotifyTextBytes = 4 << 10
)

type Notification struct {
	ID        string    `json:"id" firestore:"id"`
	MessageID string    `json:"message_id" firestore:"message_id"`
	Text      string    `json:"text" firestore:"text"`
	Urgent    bool      `json:"urgent,omitempty" firestore:"urgent,omitempty"`
	Status    string    `json:"status" firestore:"status"`
	Reason    string    `json:"reason,omitempty" firestore:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at" firestore:"created_at"`
	// SendAfter is the earliest a notification may go out: after NotifyDelay, or the end of quiet hours.
	SendAfter time.Time  `json:"send_after" firestore:"send_after"`
	SentAt    *time.Time `json:"sent_at,omitempty" firestore:"sent_at,omitempty"`
}

// NotifySeenMark is the owner's shared read position, also used to suppress notifications.
type NotifySeenMark struct {
	MessageID string    `json:"message_id" firestore:"message_id"`
	Through   time.Time `json:"through" firestore:"through"`
	// Position orders updates across snapshots and the event stream. Older saved marks have position zero.
	Position int64 `json:"position" firestore:"position"`
}

// NotifyDay counts one local day's notifications.
type NotifyDay struct {
	Day  string `firestore:"day"`
	Sent int    `firestore:"sent"`
}

// NotificationFor is the notification a newly published message gets.
func NotificationFor(m Message, urgent bool) Notification {
	text := strings.TrimSpace(m.Text)
	if len(text) > NotifyTextBytes {
		cut := NotifyTextBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "…"
	}
	return Notification{
		ID: "notify-" + m.ID, MessageID: m.ID, Text: text, Urgent: urgent, Status: NotifyPending,
		CreatedAt: m.CreatedAt, SendAfter: m.CreatedAt.Add(NotifyDelay),
	}
}

// QuietHours is a daily local window when notifications that aren't urgent wait. Start after End wraps midnight.
type QuietHours struct {
	Start, End int // minutes after midnight
	Set        bool
}

// ParseQuietHours reads "22:00-07:00". An empty value means no quiet hours.
func ParseQuietHours(value string) (QuietHours, error) {
	if strings.TrimSpace(value) == "" {
		return QuietHours{}, nil
	}
	from, to, ok := strings.Cut(value, "-")
	start, err1 := time.Parse("15:04", strings.TrimSpace(from))
	end, err2 := time.Parse("15:04", strings.TrimSpace(to))
	if !ok || err1 != nil || err2 != nil {
		return QuietHours{}, fmt.Errorf("quiet hours must look like 22:00-07:00")
	}
	return QuietHours{Start: start.Hour()*60 + start.Minute(), End: end.Hour()*60 + end.Minute(), Set: true}, nil
}

// Until returns when quiet hours that include a local time end, or zero when it's outside them.
func (q QuietHours) Until(at time.Time) time.Time {
	if !q.Set || q.Start == q.End {
		return time.Time{}
	}
	minute := at.Hour()*60 + at.Minute()
	midnight := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location())
	end := func(days int) time.Time {
		return time.Date(midnight.Year(), midnight.Month(), midnight.Day()+days, q.End/60, q.End%60, 0, 0, at.Location())
	}
	if q.Start < q.End {
		if minute >= q.Start && minute < q.End {
			return end(0)
		}
		return time.Time{}
	}
	if minute >= q.Start {
		return end(1)
	}
	if minute < q.End {
		return end(0)
	}
	return time.Time{}
}

// Notification decisions.
const (
	NotifySend = "send"
	NotifyHold = "hold"
)

// DecideNotification says what a due notification does now: send, hold until a time, or end with a status and reason.
func DecideNotification(n Notification, seen NotifySeenMark, quiet QuietHours, day NotifyDay, local time.Time) (decision string, until time.Time, reason string) {
	if !seen.Through.Before(n.CreatedAt) {
		return NotifySeen, time.Time{}, "a browser showed the message"
	}
	if n.Urgent {
		return NotifySend, time.Time{}, ""
	}
	if end := quiet.Until(local); !end.IsZero() {
		return NotifyHold, end, "quiet hours"
	}
	if day.Sent >= NotificationsPerDay {
		return NotifySkipped, time.Time{}, fmt.Sprintf("%d notifications were already sent that day", NotificationsPerDay)
	}
	return NotifySend, time.Time{}, ""
}

// Notifies reports whether a published turn's reply should reach Austin when the app is closed: it answers task
// events, not a message of his and not the introduction.
func Notifies(t ConversationTurn) bool {
	events := false
	for _, in := range t.Inputs {
		if in.IsUser() || in.Introduction {
			return false
		}
		if in.Event != nil {
			events = true
		}
	}
	return events
}
