package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"sync"
	"unicode/utf8"
)

// NotifyMailer emails the user a notification from their own connected account. It is the runtime's own message to them,
// not an email a task asked to send, so it needs no approval and goes only to their address.
type NotifyMailer struct {
	Config CalendarConfig
	// To is their address. When empty it's read once from the account's Gmail profile.
	To string

	mu      sync.Mutex
	address string
}

func (m *NotifyMailer) recipient(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.address != "" {
		return m.address, nil
	}
	if m.To != "" {
		address, err := mail.ParseAddress(m.To)
		if err != nil {
			return "", errors.New("NORI_NOTIFY_EMAIL must be one email address")
		}
		m.address = address.Address
		return m.address, nil
	}
	var profile struct {
		EmailAddress string `json:"emailAddress"`
	}
	status, err := googleCall(ctx, m.Config.client(ctx), http.MethodGet, m.Config.gmailURL()+"/users/me/profile", nil, nil, &profile)
	if err != nil || status != http.StatusOK || profile.EmailAddress == "" {
		return "", fmt.Errorf("the connected account's address couldn't be read (HTTP %d)", status)
	}
	m.address = profile.EmailAddress
	return m.address, nil
}

func (m *NotifyMailer) Send(ctx context.Context, subject, body string) error {
	to, err := m.recipient(ctx)
	if err != nil {
		return err
	}
	subject, body = cut(subject, maxEmailSubject), cut(body, maxEmailBody)
	sum := sha256.Sum256([]byte(to + "\n" + subject + "\n" + body))
	spec := map[string]string{"to": to, "subject": subject, "body": body, "action_id": "notify-" + hex.EncodeToString(sum[:16])}
	var sent struct {
		ID string `json:"id"`
	}
	from := m.Config.Account
	if _, err := mail.ParseAddress(from); err != nil {
		from = to
	}
	status, err := googleCall(ctx, m.Config.client(ctx), http.MethodPost, m.Config.gmailURL()+"/users/me/messages/send", nil,
		map[string]string{"raw": rawEmail(from, spec)}, &sent)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 || sent.ID == "" {
		return fmt.Errorf("gmail returned HTTP %d", status)
	}
	return nil
}

func cut(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}
