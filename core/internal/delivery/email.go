package delivery

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"nori/internal/scheduler"
)

// SMTP sends email through any SMTP relay (RFC 5321) with STARTTLS or implicit TLS and PLAIN authentication. A
// 250 reply to the message data means the relay accepted it; bounces arrive later at the sender and are not
// reported back here, so the receipt state is "accepted".
type SMTP struct {
	Host     string
	Port     string
	Username string
	Password string
	From     string
}

// NewSMTP returns nil when the relay is not configured, so the channel reports no_transport.
func NewSMTP(host, port, username, password, from string) *SMTP {
	if host == "" || from == "" {
		return nil
	}
	if port == "" {
		port = "587"
	}
	return &SMTP{Host: host, Port: port, Username: username, Password: password, From: from}
}

func (e *SMTP) Kind() string { return "email" }

func (e *SMTP) Send(ctx context.Context, target scheduler.Target, msg scheduler.Message) (scheduler.Receipt, error) {
	to, err := mail.ParseAddress(target.Address)
	if err != nil {
		return scheduler.Receipt{}, fmt.Errorf("%w: invalid email address", scheduler.ErrTargetInvalid)
	}
	from, err := mail.ParseAddress(e.From)
	if err != nil {
		return scheduler.Receipt{}, errors.New("the configured sender address is invalid")
	}
	id := make([]byte, 12)
	_, _ = rand.Read(id)
	domain := from.Address[strings.LastIndex(from.Address, "@")+1:]
	messageID := "<" + hex.EncodeToString(id) + "@" + domain + ">"
	body := msg.Body
	if msg.URL != "" {
		body += "\r\n\r\n" + msg.URL
	}
	var b strings.Builder
	for _, h := range [][2]string{{"From", from.String()}, {"To", to.String()}, {"Subject", mime.QEncoding.Encode("utf-8", msg.Title)},
		{"Message-ID", messageID}, {"Date", time.Now().UTC().Format(time.RFC1123Z)}, {"MIME-Version", "1.0"},
		{"Content-Type", "text/plain; charset=utf-8"}, {"Content-Transfer-Encoding", "8bit"}, {"Auto-Submitted", "auto-generated"}} {
		b.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	b.WriteString("\r\n" + strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n") + "\r\n")

	dialer := &net.Dialer{Timeout: 15 * time.Second}
	address := net.JoinHostPort(e.Host, e.Port)
	var conn net.Conn
	if e.Port == "465" {
		conn, err = tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: e.Host})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return scheduler.Receipt{}, fmt.Errorf("%w: %v", scheduler.ErrRetryLater, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	}
	client, err := smtp.NewClient(conn, e.Host)
	if err != nil {
		conn.Close()
		return scheduler.Receipt{}, fmt.Errorf("%w: %v", scheduler.ErrRetryLater, err)
	}
	defer client.Close()
	if e.Port != "465" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return scheduler.Receipt{}, errors.New("the SMTP relay does not offer STARTTLS; refusing to send in clear text")
		}
		if err := client.StartTLS(&tls.Config{ServerName: e.Host}); err != nil {
			return scheduler.Receipt{}, fmt.Errorf("%w: %v", scheduler.ErrRetryLater, err)
		}
	}
	if e.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", e.Username, e.Password, e.Host)); err != nil {
			return scheduler.Receipt{}, fmt.Errorf("the SMTP relay refused the credentials: %v", err)
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return scheduler.Receipt{}, refusal(err)
	}
	if err := client.Rcpt(to.Address); err != nil {
		return scheduler.Receipt{}, refusal(err)
	}
	w, err := client.Data()
	if err != nil {
		return scheduler.Receipt{}, refusal(err)
	}
	if _, err := w.Write([]byte(b.String())); err != nil {
		return scheduler.Receipt{}, fmt.Errorf("%w: %v", scheduler.ErrOutcomeUnknown, err)
	}
	// The reply to the end of DATA is the relay's acceptance; losing it leaves the outcome unknown.
	if err := w.Close(); err != nil {
		if strings.HasPrefix(err.Error(), "5") {
			return scheduler.Receipt{}, refusal(err)
		}
		return scheduler.Receipt{}, fmt.Errorf("%w: %v", scheduler.ErrOutcomeUnknown, err)
	}
	_ = client.Quit()
	return scheduler.Receipt{ID: messageID, State: "accepted"}, nil
}

// refusal maps an SMTP reply before DATA completed: 4xx is temporary, 5xx permanent. Nothing was accepted.
func refusal(err error) error {
	if strings.HasPrefix(err.Error(), "4") {
		return fmt.Errorf("%w: %v", scheduler.ErrRetryLater, err)
	}
	return fmt.Errorf("the SMTP relay refused the message: %v", err)
}
