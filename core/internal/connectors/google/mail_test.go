package google

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/mail"
	"strings"
	"testing"

	"nori/internal/connections"
	"nori/internal/invoke"
)

type mailTestTransport func(*http.Request) (*http.Response, error)

func (f mailTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGmailSendsTheApprovedMessageAndAttachment(t *testing.T) {
	const id = "f7f3b63f-5850-477a-86b4-2a8cced37775"
	const body = "Hello there,\n\nHere is the résumé."
	file := []byte("an uploaded PDF fixture")
	requests := 0
	client := &http.Client{Transport: mailTestTransport(func(req *http.Request) (*http.Response, error) {
		requests++
		if req.Method != http.MethodPost || req.URL.String() != gmailSendURL {
			t.Fatalf("unexpected Gmail request: %s %s", req.Method, req.URL)
		}
		var input struct{ Raw string }
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		messageBytes, err := base64.RawURLEncoding.DecodeString(input.Raw)
		if err != nil {
			t.Fatal(err)
		}
		message, err := mail.ReadMessage(bytes.NewReader(messageBytes))
		if err != nil {
			t.Fatal(err)
		}
		for header, want := range map[string]string{"From": "tester@example.test", "To": "friend@example.test", "Cc": "cc@example.test", "Bcc": "bcc@example.test"} {
			addresses, err := message.Header.AddressList(header)
			if err != nil || len(addresses) != 1 || addresses[0].Address != want {
				t.Fatalf("%s: got %v (%v), want %s", header, addresses, err, want)
			}
		}
		subject, err := (&mime.WordDecoder{}).DecodeHeader(message.Header.Get("Subject"))
		if err != nil || subject != "Résumé" {
			t.Fatalf("unexpected subject: %q (%v)", subject, err)
		}
		mediaType, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/mixed" {
			t.Fatalf("unexpected attachment message type: %q (%v)", mediaType, err)
		}
		parts := multipart.NewReader(message.Body, params["boundary"])
		text, err := parts.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		plain, err := io.ReadAll(text)
		if err != nil || strings.ReplaceAll(string(plain), "\r\n", "\n") != body {
			t.Fatalf("unexpected email body: %q (%v)", plain, err)
		}
		part, err := parts.NextPart()
		if err != nil || part.FileName() != "résumé.pdf" {
			t.Fatalf("unexpected attachment: %v (%v)", part, err)
		}
		decoded, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, part))
		if err != nil || !bytes.Equal(decoded, file) {
			t.Fatalf("unexpected attachment bytes: %q (%v)", decoded, err)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"id":"gmail-id","threadId":"gmail-thread"}`))}, nil
	})}
	call := invoke.Call{Grant: &connections.Grant{Client: client, Connection: connections.Record{
		ID: "conn_google_workspace", Account: connections.Account{Label: "tester@example.test"},
	}}, Args: map[string]any{"to": []any{"friend@example.test"}, "cc": []any{"cc@example.test"}, "bcc": []any{"bcc@example.test"},
		"subject": "Résumé", "body": body, "attachments": []any{id}}, Upload: func(_ context.Context, got string) (string, string, []byte, error) {
		if got != id {
			t.Fatalf("unexpected upload %s", got)
		}
		return "résumé.pdf", "application/pdf", file, nil
	}}
	adapter := mailAdapter{}
	prepared, err := adapter.Prepare(t.Context(), call)
	if err != nil || !prepared.RequireExplicitApproval || !strings.Contains(prepared.Summary, body) || len(prepared.Recipients) != 3 ||
		len(prepared.MaterialEffects) != 1 || !strings.Contains(prepared.MaterialEffects[0], "résumé.pdf") || requests != 0 {
		t.Fatalf("incomplete Gmail approval: %+v (%v)", prepared, err)
	}
	result := adapter.Execute(t.Context(), call, prepared)
	if result.Outcome != "confirmed" || result.Content["message_id"] != "gmail-id" || requests != 1 {
		t.Fatalf("send failed: %+v after %d requests", result, requests)
	}
	file = []byte("different undisclosed file")
	result = adapter.Execute(t.Context(), call, prepared)
	if result.Outcome != "failed" || requests != 1 {
		t.Fatalf("mail with changed attachment was sent: %+v after %d requests", result, requests)
	}
}
