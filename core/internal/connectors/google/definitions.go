package google

import (
	"encoding/json"
	"strings"

	"nori/internal/extensions"
)

const account = `"account": {"type": "string", "description": "Connected account email or connection ID. Omit to use the saved default or the only eligible account."}`

const sendUpdates = `"send_updates": {"enum": ["all", "external", "none"], "description": "Who Google notifies. Default all when there are attendees."}`

const timeFields = `"start": {"type": "string", "description": "RFC 3339 start for a timed event, or YYYY-MM-DD for an all-day event"},
	"end": {"type": "string", "description": "RFC 3339 end, or exclusive YYYY-MM-DD end for an all-day event"},
	"timezone": {"type": "string", "description": "IANA time zone for a timed event, e.g. America/Los_Angeles"}`

type spec struct {
	name, title, capability, purpose, target string
	effect                                   extensions.Effect
	retry                                    string
	description, schema                      string
}

var specs = []spec{
	{"calendar_list_calendars", "List calendars", "calendar", "calendar.read", "google.calendar/calendars.list", "read", "safe",
		"List the calendars of the user's connected Google accounts, with which are selected and writable.",
		`{"type": "object", "properties": {` + account + `}, "additionalProperties": false}`},
	{"calendar_events", "Find calendar events", "calendar", "calendar.read", "google.calendar/events.list", "read", "safe",
		"List or search events between two times on the selected calendars (or the calendars named). Results carry the account, calendar, source link and the provider revision needed for later changes.",
		`{"type": "object", "properties": {` + account + `,
			"time_min": {"type": "string", "description": "RFC 3339 instant or YYYY-MM-DD"},
			"time_max": {"type": "string", "description": "RFC 3339 instant or YYYY-MM-DD (exclusive)"},
			"calendar_ids": {"type": "array", "items": {"type": "string"}, "maxItems": 20},
			"query": {"type": "string", "maxLength": 200},
			"max_results": {"type": "integer", "minimum": 1, "maximum": 100}
		}, "required": ["time_min", "time_max"], "additionalProperties": false}`},
	{"calendar_event_get", "Read a calendar event", "calendar", "calendar.read", "google.calendar/events.get", "read", "safe",
		"Read one event's current state, including attendees, recurrence and revision.",
		`{"type": "object", "properties": {` + account + `, "calendar_id": {"type": "string"}, "event_id": {"type": "string"}}, "required": ["calendar_id", "event_id"], "additionalProperties": false}`},
	{"calendar_availability", "Check availability", "calendar", "calendar.read", "google.calendar/freebusy", "read", "safe",
		"Return busy intervals for the user's selected calendars and for attendees whose availability the account can see. Intervals Google cannot see are reported as unknown, never free.",
		`{"type": "object", "properties": {` + account + `,
			"time_min": {"type": "string"}, "time_max": {"type": "string"},
			"calendar_ids": {"type": "array", "items": {"type": "string"}, "maxItems": 20},
			"attendees": {"type": "array", "items": {"type": "string"}, "maxItems": 20}
		}, "required": ["time_min", "time_max"], "additionalProperties": false}`},
	{"calendar_event_create", "Create a calendar event", "calendar", "calendar.write", "google.calendar/events.insert", "reversible_write", "provider_key",
		"Create an event on one calendar. Inviting attendees notifies them unless send_updates is none. The result is read back from Google before it is reported.",
		`{"type": "object", "properties": {` + account + `, "calendar_id": {"type": "string", "description": "Default: the account's primary calendar"},
			"title": {"type": "string", "minLength": 1, "maxLength": 1024}, ` + timeFields + `,
			"location": {"type": "string", "maxLength": 1024}, "description": {"type": "string", "maxLength": 8192},
			"attendees": {"type": "array", "items": {"type": "string"}, "maxItems": 100},
			"recurrence": {"type": "array", "items": {"type": "string", "pattern": "^(RRULE|EXDATE|RDATE):"}, "maxItems": 10},
			"add_meeting_link": {"type": "boolean"}, ` + sendUpdates + `
		}, "required": ["title", "start", "end"], "additionalProperties": false}`},
	{"calendar_event_update", "Change a calendar event", "calendar", "calendar.write", "google.calendar/events.patch", "reversible_write", "reconcile_only",
		"Change an event's title, time, location, description or attendees. For a recurring event, scope says whether the change applies to this occurrence, this and following occurrences, or the whole series. A change made in Google after the event was read is not overwritten.",
		`{"type": "object", "properties": {` + account + `, "calendar_id": {"type": "string"}, "event_id": {"type": "string"},
			"scope": {"enum": ["instance", "following", "series"]},
			"title": {"type": "string", "maxLength": 1024}, ` + timeFields + `,
			"location": {"type": "string", "maxLength": 1024}, "description": {"type": "string", "maxLength": 8192},
			"add_attendees": {"type": "array", "items": {"type": "string"}, "maxItems": 100},
			"remove_attendees": {"type": "array", "items": {"type": "string"}, "maxItems": 100}, ` + sendUpdates + `
		}, "required": ["calendar_id", "event_id"], "additionalProperties": false}`},
	{"calendar_event_cancel", "Cancel a calendar event", "calendar", "calendar.write", "google.calendar/events.delete", "reversible_write", "reconcile_only",
		"Cancel an event, one occurrence, or this and following occurrences of a recurring event.",
		`{"type": "object", "properties": {` + account + `, "calendar_id": {"type": "string"}, "event_id": {"type": "string"},
			"scope": {"enum": ["instance", "following", "series"]}, ` + sendUpdates + `
		}, "required": ["calendar_id", "event_id"], "additionalProperties": false}`},
	{"calendar_event_respond", "Reply to an invitation", "calendar", "calendar.write", "google.calendar/events.respond", "reversible_write", "reconcile_only",
		"Accept, decline or tentatively accept an invitation as the connected account. The organizer is notified.",
		`{"type": "object", "properties": {` + account + `, "calendar_id": {"type": "string"}, "event_id": {"type": "string"},
			"response": {"enum": ["accepted", "declined", "tentative"]}, "comment": {"type": "string", "maxLength": 1000}
		}, "required": ["calendar_id", "event_id", "response"], "additionalProperties": false}`},
	{"calendar_ics_preview", "Preview an ICS file", "calendar", "calendar.read", "google.calendar/ics.preview", "read", "safe",
		"Show the events in an uploaded .ics calendar file the task was given, and which already exist on the target calendar. An imported file is not a live calendar connection.",
		`{"type": "object", "properties": {` + account + `, "upload_id": {"type": "string"}, "calendar_id": {"type": "string"}}, "required": ["upload_id"], "additionalProperties": false}`},
	{"calendar_ics_import", "Import an ICS file", "calendar", "calendar.write", "google.calendar/ics.import", "reversible_write", "provider_key",
		"Import the events of an uploaded .ics file into one calendar without sending invitations. Events that already exist (same iCalUID) are updated, not duplicated. Preview first.",
		`{"type": "object", "properties": {` + account + `, "upload_id": {"type": "string"}, "calendar_id": {"type": "string"}}, "required": ["upload_id"], "additionalProperties": false}`},
	{"mail_search", "Search email", "mail", "mail.read", "google.mail/messages.search", "read", "safe",
		"Search the connected Gmail account with Gmail query syntax (from:, to:, subject:, after:, has:attachment). Returns snippets, not full messages.",
		`{"type": "object", "properties": {` + account + `, "query": {"type": "string", "minLength": 1, "maxLength": 500}, "max_results": {"type": "integer", "minimum": 1, "maximum": 50}}, "required": ["query"], "additionalProperties": false}`},
	{"mail_read", "Read an email or thread", "mail", "mail.read", "google.mail/messages.get", "read", "safe",
		"Read a message's full text, or every message of a thread, with sender, recipients and attachments.",
		`{"type": "object", "properties": {` + account + `, "message_id": {"type": "string"}, "thread_id": {"type": "string"}}, "additionalProperties": false}`},
	{"mail_draft_create", "Draft an email", "mail", "mail.draft", "google.mail/drafts.create", "reversible_write", "reconcile_only",
		"Create a draft in the connected account. A draft is not sent. For a reply, give reply_to_message_id and mode reply or reply_all.",
		`{"type": "object", "properties": {` + account + `,
			"to": {"type": "array", "items": {"type": "string"}, "maxItems": 100}, "cc": {"type": "array", "items": {"type": "string"}, "maxItems": 100},
			"bcc": {"type": "array", "items": {"type": "string"}, "maxItems": 100}, "subject": {"type": "string", "maxLength": 998},
			"body": {"type": "string", "maxLength": 65536}, "reply_to_message_id": {"type": "string"},
			"mode": {"enum": ["new", "reply", "reply_all", "forward"]}
		}, "required": ["body"], "additionalProperties": false}`},
	{"mail_draft_update", "Edit a draft", "mail", "mail.draft", "google.mail/drafts.update", "reversible_write", "reconcile_only",
		"Replace a draft's recipients, subject or body. Approval of a send binds to the draft's final content.",
		`{"type": "object", "properties": {` + account + `, "draft_id": {"type": "string"},
			"to": {"type": "array", "items": {"type": "string"}, "maxItems": 100}, "cc": {"type": "array", "items": {"type": "string"}, "maxItems": 100},
			"bcc": {"type": "array", "items": {"type": "string"}, "maxItems": 100}, "subject": {"type": "string", "maxLength": 998},
			"body": {"type": "string", "maxLength": 65536}
		}, "required": ["draft_id", "body"], "additionalProperties": false}`},
	{"mail_send_draft", "Send a draft", "mail", "mail.send", "google.mail/drafts.send", "sensitive_send", "reconcile_only",
		"Send an existing draft exactly as it is. The approval shows the sender, recipients and content that will be sent.",
		`{"type": "object", "properties": {` + account + `, "draft_id": {"type": "string"}}, "required": ["draft_id"], "additionalProperties": false}`},
	{"mail_modify", "Organize email", "mail", "mail.organize", "google.mail/messages.modify", "reversible_write", "reconcile_only",
		"Add or remove labels, archive, or mark read or unread for up to 50 reviewed messages.",
		`{"type": "object", "properties": {` + account + `, "message_ids": {"type": "array", "items": {"type": "string"}, "minItems": 1, "maxItems": 50},
			"add_labels": {"type": "array", "items": {"type": "string"}, "maxItems": 10}, "remove_labels": {"type": "array", "items": {"type": "string"}, "maxItems": 10},
			"archive": {"type": "boolean"}, "mark_read": {"type": "boolean"}, "mark_unread": {"type": "boolean"}
		}, "required": ["message_ids"], "additionalProperties": false}`},
	{"contacts_search", "Look up contacts", "contacts", "contacts.read", "google.contacts/search", "read", "safe",
		"Find people by name or email in the connected account's contacts and other contacts. Returns candidates with their source; an ambiguous name must be confirmed with the user, never guessed.",
		`{"type": "object", "properties": {` + account + `, "query": {"type": "string", "minLength": 1, "maxLength": 200}}, "required": ["query"], "additionalProperties": false}`},
}

var capabilityTitles = map[string]string{"calendar": "Google Calendar", "mail": "Gmail", "contacts": "Google Contacts"}

var definitions = func() []extensions.Definition {
	out := make([]extensions.Definition, 0, len(specs))
	for _, s := range specs {
		var compact any
		if err := json.Unmarshal([]byte(s.schema), &compact); err != nil {
			panic("google tool schema " + s.name + ": " + err.Error())
		}
		schema, _ := json.Marshal(compact)
		timeout := 30_000
		if s.effect != extensions.EffectRead {
			timeout = 45_000
		}
		d, err := extensions.Seal(extensions.Definition{
			Ref:       extensions.DefinitionRef{ID: "google/" + s.capability + "/" + strings.ReplaceAll(s.name, "_", "-"), Version: Version},
			Extension: extensions.DefinitionRef{ID: "google/" + s.capability, Version: Version},
			ModelName: s.name, Title: s.title, Description: s.description,
			Capability: "google_" + s.capability, CapabilityTitle: capabilityTitles[s.capability], IntegrationID: IntegrationID,
			InputSchema: schema, Binding: extensions.Binding{Adapter: extensions.AdapterNative, Target: s.target, Version: Version},
			ConnectionType: IntegrationID, RequiredCapability: s.purpose,
			Policy:   extensions.Policy{Effect: s.effect, Retry: s.retry, TimeoutMS: timeout, Location: extensions.LocationRuntime},
			Exposure: extensions.ExposureDirect,
		})
		if err != nil {
			panic(err)
		}
		out = append(out, d)
	}
	return out
}()
