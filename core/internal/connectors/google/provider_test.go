package google

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"nori/internal/connections"
	"nori/internal/extensions"
	"nori/internal/invoke"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fixtureResponse(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func fixtureCall(target string, args map[string]any, f fixtureTransport) invoke.Call {
	return invoke.Call{RunID: "run", Revision: 1, OperationID: "op-1", Exec: extensions.Exec{Tool: extensions.DefinitionRef{ID: "google/test/tool", Digest: "digest"}, Binding: extensions.Binding{Target: target}}, Args: args,
		Grant: &connections.Grant{Connection: connections.Record{ID: "conn"}, Client: &http.Client{Transport: f}}, TimeZone: "America/Los_Angeles"}
}

func TestGoogleScopesAndCapabilities(t *testing.T) {
	info := (Integration{ClientID: "id", ClientSecret: "secret"}).Info()
	want := map[string]string{"calendar.read": scopeCalendarRead, "calendar.write": scopeCalendarWrite, "files.read": scopeDriveFile, "files.write": scopeDriveFile, "mail.send": scopeMailSend}
	for _, cap := range info.Capabilities {
		scope, ok := want[cap.ID]
		if !ok {
			t.Fatalf("unexpected capability %s", cap.ID)
		}
		found := false
		for _, got := range cap.Scopes {
			if got == scope {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s does not include %s", cap.ID, scope)
		}
		delete(want, cap.ID)
	}
	if len(want) != 0 || info.MultipleAccounts {
		t.Fatalf("capabilities missing %v or multiple accounts enabled", want)
	}
}

func TestCalendarPaginationLocalDateAndApprovalIdentity(t *testing.T) {
	pages := 0
	transport := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/calendarList"):
			pages++
			if pages == 1 {
				return fixtureResponse(200, `{"items":[{"id":"primary-id","summary":"Personal","primary":true,"accessRole":"owner"}],"nextPageToken":"next"}`), nil
			}
			if pages == 2 && r.URL.Query().Get("pageToken") == "next" {
				return fixtureResponse(200, `{"items":[{"id":"shared","summary":"Shared","accessRole":"writer"}]}`), nil
			}
			t.Fatalf("unexpected page %d", pages)
		case strings.HasSuffix(r.URL.Path, "/events") && r.Method == "GET":
			if got := r.URL.Query().Get("timeMin"); got != "2026-11-25T00:00:00-08:00" {
				t.Fatalf("local date became %s", got)
			}
			return fixtureResponse(200, `{"items":[]}`), nil
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL)
		return nil, nil
	})
	call := fixtureCall("google.calendar/events.list", map[string]any{"time_min": "2026-11-25", "time_max": "2026-11-26"}, transport)
	resources, err := listCalendars(context.Background(), call.Grant.Client)
	if err != nil || len(resources) != 2 || !resources[1].Writable || pages != 2 {
		t.Fatalf("calendar list %v pages=%d err=%v", resources, pages, err)
	}
	p, err := (calendarAdapter{}).Prepare(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if got := (calendarAdapter{}).Execute(context.Background(), call, p); got.Err != nil {
		t.Fatal(got.Err)
	}
	call.Exec.Binding.Target = "google.calendar/events.insert"
	call.Args = map[string]any{"title": "Review", "start": "2026-11-25T10:00:00-08:00", "end": "2026-11-25T11:00:00-08:00", "timezone": "America/Los_Angeles", "attendees": []any{"person@example.com"}}
	first, err := (calendarAdapter{}).Prepare(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	call.OperationID = "op-2"
	second, err := (calendarAdapter{}).Prepare(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if !first.RequireExplicitApproval || first.Keys["event_id"] == second.Keys["event_id"] {
		t.Fatalf("approval=%v keys=%v %v", first.RequireExplicitApproval, first.Keys, second.Keys)
	}
	if invoke.Digest(call, "conn", 1, first) != invoke.Digest(call, "conn", 1, second) {
		t.Fatal("provider IDs changed the approval digest")
	}
	if !strings.Contains(first.Summary, "person@example.com") || !strings.Contains(first.Summary, "2026-11-25T10:00:00-08:00") {
		t.Fatalf("incomplete preview %q", first.Summary)
	}
}

func TestCalendarUpdatePreviewAndConditionalWrite(t *testing.T) {
	var changed bool
	transport := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" {
			return fixtureResponse(200, `{"id":"evt","etag":"\"v1\"","summary":"Old title","start":{"dateTime":"2026-11-25T10:00:00-08:00"},"end":{"dateTime":"2026-11-25T11:00:00-08:00"},"attendees":[{"email":"old@example.com"}]}`), nil
		}
		if r.Method == "PATCH" {
			if r.Header.Get("If-Match") != `"v1"` {
				t.Fatalf("missing provider revision: %q", r.Header.Get("If-Match"))
			}
			changed = true
			return fixtureResponse(200, `{"id":"evt","etag":"\"v2\"","summary":"New title"}`), nil
		}
		t.Fatalf("unexpected method %s", r.Method)
		return nil, nil
	})
	call := fixtureCall("google.calendar/events.patch", map[string]any{"calendar_id": "shared", "event_id": "evt", "title": "New title", "description": "New description", "location": "Room 2", "attendees": []any{"new@example.com"}}, transport)
	p, err := (calendarAdapter{}).Prepare(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"New title", "New description", "Room 2", "old@example.com", "new@example.com"} {
		if !strings.Contains(p.Summary, text) {
			t.Fatalf("preview omitted %q: %q", text, p.Summary)
		}
	}
	if len(p.Recipients) != 2 || !p.RequireExplicitApproval {
		t.Fatalf("recipients=%v approval=%v", p.Recipients, p.RequireExplicitApproval)
	}
	result := (calendarAdapter{}).Execute(context.Background(), call, p)
	if result.Err != nil || !changed {
		t.Fatalf("execute: %v", result.Err)
	}
}

func TestSheetUpdateTargetsOneRangeAndChecksRevision(t *testing.T) {
	var putCount int
	version := "12"
	transport := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "www.googleapis.com" && r.Method == "GET" {
			return fixtureResponse(200, `{"id":"file","name":"Budget","mimeType":"application/vnd.google-apps.spreadsheet","version":"`+version+`","capabilities":{"canEdit":true,"canTrash":true}}`), nil
		}
		if r.URL.Host == "sheets.googleapis.com" && r.Method == "GET" {
			return fixtureResponse(200, `{"sheets":[{"properties":{"title":"Budget"}},{"properties":{"title":"Forecast"}}]}`), nil
		}
		if r.URL.Host == "sheets.googleapis.com" && r.Method == "PUT" {
			putCount++
			if !strings.Contains(r.URL.Path, "'Budget'!B2:C3") || r.URL.Query().Get("valueInputOption") != "RAW" {
				t.Fatalf("unexpected target %s", r.URL)
			}
			var body struct {
				Values [][]string `json:"values"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body.Values) != 2 || body.Values[1][1] != "4" {
				t.Fatalf("bad values: %v", body.Values)
			}
			return fixtureResponse(200, `{"updatedCells":4}`), nil
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL)
		return nil, nil
	})
	call := fixtureCall("google.drive/files.update", map[string]any{"file_id": "file", "sheet": "Budget", "range": "B2", "content": "1,2\n3,4"}, transport)
	p, err := (driveAdapter{}).Prepare(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if !p.RequireExplicitApproval || !strings.Contains(p.Summary, "1,2\n3,4") {
		t.Fatalf("preview %q", p.Summary)
	}
	result := (driveAdapter{}).Execute(context.Background(), call, p)
	if result.Err != nil || putCount != 1 {
		t.Fatalf("result=%v writes=%d", result.Err, putCount)
	}
	version = "13"
	result = (driveAdapter{}).Execute(context.Background(), call, p)
	if result.Err == nil || putCount != 1 {
		t.Fatalf("stale version allowed: %v writes=%d", result.Err, putCount)
	}
}

func TestDocUpdateUsesRequiredRevisionAndDisplaysChanges(t *testing.T) {
	transport := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "www.googleapis.com" {
			return fixtureResponse(200, `{"id":"doc","name":"Plan","mimeType":"application/vnd.google-apps.document","version":"7","capabilities":{"canEdit":true}}`), nil
		}
		if r.URL.Host == "docs.googleapis.com" && r.Method == "GET" {
			return fixtureResponse(200, `{"revisionId":"doc-revision"}`), nil
		}
		if r.URL.Host == "docs.googleapis.com" && r.Method == "POST" {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["writeControl"].(map[string]any)["requiredRevisionId"] != "doc-revision" {
				t.Fatalf("missing revision in %v", body)
			}
			return fixtureResponse(200, `{"replies":[{"replaceAllText":{"occurrencesChanged":1}}]}`), nil
		}
		t.Fatalf("unexpected request %s %s", r.Method, r.URL)
		return nil, nil
	})
	call := fixtureCall("google.drive/files.update", map[string]any{"file_id": "doc", "find": "old text", "replace": "new text"}, transport)
	p, err := (driveAdapter{}).Prepare(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if !p.RequireExplicitApproval || !strings.Contains(p.Summary, "old text") || !strings.Contains(p.Summary, "new text") {
		t.Fatalf("preview %q", p.Summary)
	}
	if result := (driveAdapter{}).Execute(context.Background(), call, p); result.Err != nil {
		t.Fatal(result.Err)
	}
}
