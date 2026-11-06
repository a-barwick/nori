package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"nori/internal/prompts"
)

// A write has three steps. Prepare validates it and reads the provider, so the user
// approves what is there now. Send runs the saved action once. Check reads the provider
// without sending, and is the only step allowed after a lost response or a dead worker.

// Write outcomes, as saved on the operation record.
const (
	WriteConfirmed = "confirmed"
	// WriteSubmitted means the provider accepted the write, and a read of it didn't complete.
	WriteSubmitted = "submitted"
	WriteFailed    = "failed"
	WriteUncertain = "uncertain"
)

// PreparedWrite is either an action to approve or a Result that needs no approval: the
// provider already shows the effect, or the action was refused before asking.
type PreparedWrite struct {
	Text, Digest string
	Spec         map[string]string
	Result       map[string]any
}

// WriteEffect returns the effect class of a tool that needs approval, or "".
func WriteEffect(name string) string {
	switch name {
	case "calendar_create", "calendar_move":
		return "reversible_write"
	case "email_send":
		return "sensitive_send"
	}
	return ""
}

func actionDigest(tool, account string, spec map[string]string) string {
	// encoding/json sorts map keys, so the same action always has the same digest.
	data, _ := json.Marshal(map[string]any{"tool": tool, "account": account, "spec": spec})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// PrepareWrite never changes anything at the provider.
func (s *Service) PrepareWrite(ctx context.Context, name string, args map[string]any) (PreparedWrite, error) {
	config := s.Capabilities.Calendar
	if !config.connected() {
		return PreparedWrite{Result: map[string]any{"available": false, "reason": "The Google account is not connected. " + calendarConnectHint}}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, calendarTimeout)
	defer cancel()
	text := func(key string) string { value, _ := args[key].(string); return strings.TrimSpace(value) }
	var prepared PreparedWrite
	var err error
	switch name {
	case "calendar_create":
		prepared, err = s.prepareEvent(ctx, text("title"), text("start"), text("end"), text("description"), text("private") == "true")
	case "calendar_move":
		prepared, err = s.prepareMove(ctx, text("event_id"), text("start"), text("end"))
	case "email_send":
		prepared, err = s.prepareEmail(ctx, text("to"), text("subject"), text("body"))
	default:
		return prepared, errors.New("unknown write tool")
	}
	if errors.Is(err, errCalendarDisconnected) {
		return PreparedWrite{Result: map[string]any{"available": false, "reason": "The Google connection has expired. " + calendarConnectHint}}, nil
	}
	if err == nil && prepared.Result == nil {
		prepared.Digest = actionDigest(name, config.Account, prepared.Spec)
	}
	return prepared, err
}

// SendWrite runs a saved action. ctx must outlive a Stop: a write that started finishes
// and reports what happened.
func (s *Service) SendWrite(ctx context.Context, name string, spec map[string]string) (string, map[string]any) {
	if s.targetChanged(spec) {
		return WriteFailed, map[string]any{"applied": false, "reason": "The connected account or write calendar changed after the action was prepared, so this was not sent."}
	}
	ctx, cancel := context.WithTimeout(ctx, calendarTimeout)
	defer cancel()
	switch name {
	case "calendar_create":
		return s.sendEvent(ctx, spec)
	case "calendar_move":
		return s.sendMove(ctx, spec)
	case "email_send":
		return s.sendEmail(ctx, spec)
	}
	return WriteFailed, map[string]any{"error": "unknown write tool"}
}

// CheckWrite reads the provider and sends nothing.
func (s *Service) CheckWrite(ctx context.Context, name string, spec map[string]string) (string, map[string]any) {
	if s.targetChanged(spec) {
		return WriteUncertain, map[string]any{"reason": "The connected account or write calendar changed, so the provider can't be read for this write."}
	}
	ctx, cancel := context.WithTimeout(ctx, calendarTimeout)
	defer cancel()
	switch name {
	case "calendar_create":
		return s.checkEvent(ctx, spec, "")
	case "calendar_move":
		return s.checkMove(ctx, spec, "")
	case "email_send":
		return s.checkEmail(ctx, spec)
	}
	return WriteFailed, map[string]any{"error": "unknown write tool"}
}

// targetChanged compares the saved action with this worker's account and write calendar.
func (s *Service) targetChanged(spec map[string]string) bool {
	config := s.Capabilities.Calendar
	if spec["account"] != config.Account {
		return true
	}
	calendar, onCalendar := spec["calendar_id"]
	return onCalendar && calendar != config.target()
}

// googleCall makes one provider request. Status 0 means no response arrived, so the
// request may or may not have taken effect.
func googleCall(ctx context.Context, client *http.Client, method, address string, headers map[string]string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return 0, errors.New("provider request could not be encoded")
		}
		body = strings.NewReader(string(data))
	}
	req, err := http.NewRequestWithContext(ctx, method, address, body)
	if err != nil {
		return 0, errors.New("provider request could not be built")
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	res, err := client.Do(req)
	if err != nil {
		var retrieve *oauth2.RetrieveError
		if errors.As(err, &retrieve) {
			return http.StatusUnauthorized, errCalendarDisconnected
		}
		return 0, errors.New("provider request failed before a response arrived")
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return res.StatusCode, errCalendarDisconnected
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 && out != nil {
		if json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(out) != nil {
			return res.StatusCode, errors.New("provider returned an unreadable response")
		}
	}
	return res.StatusCode, nil
}

// refusedByProvider is a definite answer: the provider rejected the request, so nothing changed.
func refusedByProvider(status int) bool {
	return status >= 400 && status < 500 && status != http.StatusRequestTimeout && status != http.StatusConflict && status != http.StatusTooManyRequests
}

func refusal(status int) map[string]any {
	output := map[string]any{"applied": false, "provider_status": status}
	switch status {
	case http.StatusUnauthorized:
		output["available"], output["reason"] = false, "The Google connection has expired. "+calendarConnectHint
	case http.StatusForbidden:
		output["available"], output["reason"] = false, "The connected account hasn't granted this. "+calendarConnectHint
	case http.StatusPreconditionFailed:
		output["reason"] = prompts.Runtime("event-changed", nil)
	default:
		output["reason"] = "The provider refused this request."
	}
	return output
}

func (s *Service) when(at time.Time) string {
	return at.In(s.location()).Format("Mon Jan 2, 3:04 PM MST")
}
