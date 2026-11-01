package agent

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"
)

const (
	gmailAPI        = "https://gmail.googleapis.com/gmail/v1"
	maxEmailSubject = 512
	maxEmailBody    = 8 << 10
	// The metadata scope can't search, so a lost send is looked for in the latest sent mail.
	sentMailChecked = 10
)

func (c CalendarConfig) gmailURL() string {
	if c.APIBase != "" {
		return c.APIBase + "/gmail/v1"
	}
	return gmailAPI
}

type sentMessage struct {
	ID       string   `json:"id"`
	LabelIDs []string `json:"labelIds"`
	Payload  struct {
		Headers []struct{ Name, Value string } `json:"headers"`
	} `json:"payload"`
}

func (m sentMessage) header(name string) string {
	for _, header := range m.Payload.Headers {
		if strings.EqualFold(header.Name, name) {
			return header.Value
		}
	}
	return ""
}

func (s *Service) prepareEmail(ctx context.Context, to, subject, body string) (PreparedWrite, error) {
	address, err := mail.ParseAddress(to)
	if err != nil || strings.ContainsAny(to, ",;") {
		return PreparedWrite{}, errors.New("email_send needs one recipient address")
	}
	if subject == "" || len(subject) > maxEmailSubject || strings.ContainsAny(subject, "\r\n") || body == "" || len(body) > maxEmailBody {
		return PreparedWrite{}, errors.New("email_send needs a one-line subject of 1–512 bytes and a body of 1–8192 bytes")
	}
	config := s.Capabilities.Calendar
	sum := sha256.Sum256([]byte(strings.Join([]string{config.Account, address.Address, subject, body}, "\n")))
	spec := map[string]string{"to": address.Address, "subject": subject, "body": body, "message_id": "<" + hex.EncodeToString(sum[:16]) + "@noribot.chat>"}
	// The same message can't go out twice. The user asks for a repeat by changing the text.
	found, status, err := s.findSent(ctx, spec["message_id"])
	if err != nil {
		return PreparedWrite{}, err
	}
	if status != http.StatusOK {
		return PreparedWrite{}, fmt.Errorf("gmail returned HTTP %d", status)
	}
	if found != nil {
		return PreparedWrite{Result: map[string]any{"already_done": true, "message_id": found.ID, "to": address.Address, "subject": subject, "account": config.Account, "read_back": true}}, nil
	}
	return PreparedWrite{
		Text: "Send this email?\nFrom: " + config.Account + "\nTo: " + address.Address + "\nSubject: " + subject + "\n\n" + body,
		Spec: spec,
	}, nil
}

// findSent reads headers only, and returns nil when the message isn't in the latest sent mail.
func (s *Service) findSent(ctx context.Context, messageID string) (*sentMessage, int, error) {
	config := s.Capabilities.Calendar
	client := config.client(ctx)
	var list struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	query := url.Values{"labelIds": {"SENT"}, "maxResults": {fmt.Sprint(sentMailChecked)}}
	status, err := googleCall(ctx, client, http.MethodGet, config.gmailURL()+"/users/me/messages?"+query.Encode(), nil, nil, &list)
	if err != nil || status != http.StatusOK {
		return nil, status, err
	}
	for _, item := range list.Messages {
		message, status, err := s.readSent(ctx, client, item.ID)
		if err != nil || status != http.StatusOK {
			return nil, status, err
		}
		if message.header("Message-Id") == messageID {
			return &message, http.StatusOK, nil
		}
	}
	return nil, http.StatusOK, nil
}

func (s *Service) readSent(ctx context.Context, client *http.Client, id string) (sentMessage, int, error) {
	config := s.Capabilities.Calendar
	var message sentMessage
	query := url.Values{"format": {"metadata"}, "metadataHeaders": {"Message-Id", "To", "Subject"}}
	status, err := googleCall(ctx, client, http.MethodGet, config.gmailURL()+"/users/me/messages/"+url.PathEscape(id)+"?"+query.Encode(), nil, nil, &message)
	return message, status, err
}

func rawEmail(account string, spec map[string]string) string {
	headers := []string{
		"From: " + account, "To: " + spec["to"], "Subject: " + mime.QEncoding.Encode("utf-8", spec["subject"]),
		"Message-Id: " + spec["message_id"], "Date: " + time.Now().Format(time.RFC1123Z), "MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8", "Content-Transfer-Encoding: base64",
	}
	body := base64.StdEncoding.EncodeToString([]byte(spec["body"]))
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(headers, "\r\n") + "\r\n\r\n" + body + "\r\n"))
}

func (s *Service) sendEmail(ctx context.Context, spec map[string]string) (string, map[string]any) {
	config := s.Capabilities.Calendar
	var sent struct {
		ID string `json:"id"`
	}
	client := config.client(ctx)
	status, _ := googleCall(ctx, client, http.MethodPost, config.gmailURL()+"/users/me/messages/send", nil, map[string]string{"raw": rawEmail(config.Account, spec)}, &sent)
	if refusedByProvider(status) {
		return WriteFailed, refusal(status)
	}
	if status < 200 || status >= 300 || sent.ID == "" {
		return s.checkEmail(ctx, spec)
	}
	output := map[string]any{"provider_status": fmt.Sprint(status), "message_id": sent.ID, "to": spec["to"], "subject": spec["subject"], "account": config.Account, "read_back": false}
	if message, read, err := s.readSent(ctx, client, sent.ID); err == nil && read == http.StatusOK {
		if message.header("Message-Id") != spec["message_id"] {
			output["reason"] = "The provider's sent message doesn't match the approved one."
			return WriteUncertain, output
		}
		output["read_back"], output["labels"] = true, message.LabelIDs
		return WriteConfirmed, output
	}
	return WriteSubmitted, output
}

// checkEmail never sends. A message missing from the latest sent mail is reported as not sent.
func (s *Service) checkEmail(ctx context.Context, spec map[string]string) (string, map[string]any) {
	found, status, err := s.findSent(ctx, spec["message_id"])
	if err != nil || status != http.StatusOK {
		return WriteUncertain, map[string]any{"to": spec["to"], "reason": "The sent mail couldn't be read, so this email may or may not have gone out."}
	}
	if found == nil {
		return WriteFailed, map[string]any{"applied": false, "to": spec["to"], "reason": "The email isn't in the latest sent mail, so it wasn't sent."}
	}
	return WriteConfirmed, map[string]any{"message_id": found.ID, "to": spec["to"], "subject": spec["subject"], "account": s.Capabilities.Calendar.Account, "read_back": true}
}
