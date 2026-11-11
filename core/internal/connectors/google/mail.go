package google

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
	"google.golang.org/api/people/v1"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/state"
)

type mailAdapter struct {
	// selfOnly sends only to the connected account's own address (settings.Connections.MailRecipients).
	selfOnly bool
}

// othersThanSelf returns the recipients that are not the connected account's own address. The address is the account
// label, which Identify reads from the account's profile at connect; it never comes from the model or a setting.
func othersThanSelf(call invoke.Call, recipients []string) []string {
	self := ""
	if call.Grant != nil {
		self = strings.ToLower(strings.TrimSpace(call.Grant.Connection.Account.Label))
	}
	others := []string{}
	for _, r := range recipients {
		if self == "" || strings.ToLower(strings.TrimSpace(r)) != self {
			others = append(others, r)
		}
	}
	return others
}

const maxMailText = 32 << 10

func gmailService(ctx context.Context, call invoke.Call) (*gmail.Service, error) {
	if call.Grant == nil {
		return nil, errors.New("email tools need a connected account")
	}
	return gmail.NewService(ctx, option.WithHTTPClient(call.Grant.Client))
}

func header(headers []*gmail.MessagePartHeader, name string) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}

func addresses(value string) []map[string]any {
	list, err := mail.ParseAddressList(value)
	out := []map[string]any{}
	if err != nil {
		for _, part := range strings.Split(value, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, map[string]any{"email": part})
			}
		}
		return out
	}
	for _, a := range list {
		entry := map[string]any{"email": a.Address}
		if a.Name != "" {
			entry["name"] = a.Name
		}
		out = append(out, entry)
	}
	return out
}

func bodyText(part *gmail.MessagePart) string {
	if part == nil {
		return ""
	}
	if strings.HasPrefix(part.MimeType, "text/plain") && part.Body != nil && part.Body.Data != "" {
		data, err := base64.URLEncoding.DecodeString(part.Body.Data)
		if err != nil {
			data, _ = base64.RawURLEncoding.DecodeString(part.Body.Data)
		}
		return string(data)
	}
	for _, child := range part.Parts {
		if text := bodyText(child); text != "" {
			return text
		}
	}
	return ""
}

func attachments(part *gmail.MessagePart, out *[]map[string]any) {
	if part == nil {
		return
	}
	if part.Filename != "" && part.Body != nil && part.Body.AttachmentId != "" {
		*out = append(*out, map[string]any{"id": part.Body.AttachmentId, "name": part.Filename, "media_type": part.MimeType, "size": part.Body.Size})
	}
	for _, child := range part.Parts {
		attachments(child, out)
	}
}

func projectMessage(message *gmail.Message, connection string, full bool) map[string]any {
	var headers []*gmail.MessagePartHeader
	if message.Payload != nil {
		headers = message.Payload.Headers
	}
	received := time.UnixMilli(message.InternalDate).UTC().Format(time.RFC3339)
	content := map[string]any{"kind": "snippet", "text": message.Snippet}
	if full {
		text := bodyText(message.Payload)
		if len(text) > maxMailText {
			text = text[:maxMailText] + "\n…(message shortened)"
		}
		content = map[string]any{"kind": "full", "text": text}
	}
	files := []map[string]any{}
	attachments(message.Payload, &files)
	out := map[string]any{
		"ref":         map[string]any{"connection_id": connection, "kind": "mail_message", "provider_id": message.Id},
		"thread":      map[string]any{"connection_id": connection, "kind": "mail_thread", "provider_id": message.ThreadId},
		"from":        firstAddress(header(headers, "From")),
		"to":          addresses(header(headers, "To")),
		"subject":     header(headers, "Subject"),
		"received_at": received,
		"content":     content,
		"labels":      message.LabelIds,
		"source_url":  "https://mail.google.com/mail/u/0/#all/" + message.ThreadId,
	}
	if cc := header(headers, "Cc"); cc != "" {
		out["cc"] = addresses(cc)
	}
	if len(files) > 0 {
		out["attachments"] = files
	}
	return out
}

func firstAddress(value string) map[string]any {
	list := addresses(value)
	if len(list) == 0 {
		return map[string]any{"email": value}
	}
	return list[0]
}

// messageID is the RFC 5322 Message-ID Nori assigns a draft, derived from the operation so an interrupted attempt
// can be found again with Gmail's rfc822msgid search.
func messageID(operationID string) string {
	sum := sha256.Sum256([]byte("nori-mail:" + operationID))
	return "<nori-" + hex.EncodeToString(sum[:16]) + "@mail.nori.invalid>"
}

func (a mailAdapter) Prepare(ctx context.Context, call invoke.Call) (invoke.Prepared, error) {
	_, operation, _ := strings.Cut(call.Exec.Binding.Target, "/")
	args := call.Args
	conn := connectionID(call)
	switch operation {
	case "messages.search":
		return invoke.Prepared{Summary: "Search email: " + invoke.String(args, "query"), Effect: extensions.EffectRead}, nil
	case "messages.get":
		if invoke.String(args, "message_id") == "" && invoke.String(args, "thread_id") == "" {
			return invoke.Prepared{}, fmt.Errorf("%w: give message_id or thread_id", invoke.ErrInvalidArguments)
		}
		return invoke.Prepared{Summary: "Read email", Effect: extensions.EffectRead}, nil
	case "drafts.create", "drafts.update":
		to, cc, bcc := invoke.Strings(args, "to"), invoke.Strings(args, "cc"), invoke.Strings(args, "bcc")
		for _, list := range [][]string{to, cc, bcc} {
			for _, address := range list {
				if _, err := mail.ParseAddress(address); err != nil {
					return invoke.Prepared{}, fmt.Errorf("%w: %q is not an email address", invoke.ErrInvalidArguments, address)
				}
			}
		}
		recipients := append(append(append([]string{}, to...), cc...), bcc...)
		sort.Strings(recipients)
		body := invoke.String(args, "body")
		sum := sha256.Sum256([]byte(body))
		verb := "Draft"
		if operation == "drafts.update" {
			verb = "Edit draft"
		}
		return preparedWithKeys{
			Summary: draftSummary(verb, draftSubject(ctx, call), recipients),
			Effect:  extensions.EffectReversibleWrite, Recipients: recipients,
			Spec: map[string]string{"to": strings.Join(to, ","), "cc": strings.Join(cc, ","), "bcc": strings.Join(bcc, ","),
				"subject": invoke.String(args, "subject"), "body_sha256": hex.EncodeToString(sum[:]), "reply_to": invoke.String(args, "reply_to_message_id"),
				"mode": invoke.String(args, "mode"), "draft": invoke.String(args, "draft_id")},
		}.withKey("message_id", call), nil
	case "drafts.send":
		service, err := gmailService(ctx, call)
		if err != nil {
			return invoke.Prepared{}, err
		}
		draft, err := service.Users.Drafts.Get("me", invoke.String(args, "draft_id")).Format("full").Context(ctx).Do()
		if err != nil {
			if definite(err) {
				return invoke.Prepared{}, fmt.Errorf("%w: the draft could not be read: %v", invoke.ErrInvalidArguments, classify(err))
			}
			return invoke.Prepared{}, classify(err)
		}
		headers := draft.Message.Payload.Headers
		recipients := []string{}
		for _, name := range []string{"To", "Cc", "Bcc"} {
			for _, a := range addresses(header(headers, name)) {
				recipients = append(recipients, a["email"].(string))
			}
		}
		if len(recipients) == 0 {
			return invoke.Prepared{}, fmt.Errorf("%w: the draft has no recipients", invoke.ErrInvalidArguments)
		}
		if others := othersThanSelf(call, recipients); a.selfOnly && len(others) > 0 {
			return invoke.Prepared{}, fmt.Errorf("%w: this Nori sends email only to the connected account's own address (%s); the draft is also addressed to %s. Change the draft or tell the user",
				invoke.ErrInvalidArguments, call.Grant.Connection.Account.Label, strings.Join(others, ", "))
		}
		sort.Strings(recipients)
		body := bodyText(draft.Message.Payload)
		sum := sha256.Sum256([]byte(body))
		preview := body
		if len(preview) > 600 {
			preview = preview[:600] + "…"
		}
		return invoke.Prepared{
			Summary: fmt.Sprintf("Send %q from %s to %s:\n%s", header(headers, "Subject"), call.Grant.Connection.Account.Label, strings.Join(recipients, ", "), preview),
			Effect:  extensions.EffectSensitiveSend, Recipients: recipients, Precondition: draft.Message.Id,
			Resources: []state.ProviderRef{{ConnectionID: conn, Kind: "mail_draft", ProviderID: draft.Id, ProviderRevision: draft.Message.Id}},
			Spec: map[string]string{"draft": draft.Id, "sender": call.Grant.Connection.Account.Label, "subject": header(headers, "Subject"),
				"recipients": strings.Join(recipients, ","), "body_sha256": hex.EncodeToString(sum[:]), "message_id": header(headers, "Message-Id")},
			MaterialEffects: []string{"The message is delivered to its recipients and cannot be recalled."},
		}, nil
	case "messages.modify":
		ids := invoke.Strings(args, "message_ids")
		sort.Strings(ids)
		add, remove := invoke.Strings(args, "add_labels"), invoke.Strings(args, "remove_labels")
		if invoke.Bool(args, "archive") {
			remove = append(remove, "INBOX")
		}
		if invoke.Bool(args, "mark_read") {
			remove = append(remove, "UNREAD")
		}
		if invoke.Bool(args, "mark_unread") {
			add = append(add, "UNREAD")
		}
		if len(add)+len(remove) == 0 {
			return invoke.Prepared{}, fmt.Errorf("%w: nothing to change", invoke.ErrInvalidArguments)
		}
		resources := make([]state.ProviderRef, 0, len(ids))
		for _, id := range ids {
			resources = append(resources, state.ProviderRef{ConnectionID: conn, Kind: "mail_message", ProviderID: id})
		}
		return invoke.Prepared{
			Summary: fmt.Sprintf("Change %d message(s): add %v, remove %v", len(ids), add, remove), Effect: extensions.EffectReversibleWrite,
			Resources: resources, Spec: map[string]string{"ids": strings.Join(ids, ","), "add": strings.Join(add, ","), "remove": strings.Join(remove, ",")},
		}, nil
	}
	return invoke.Prepared{}, fmt.Errorf("%w: unknown mail operation", invoke.ErrInvalidArguments)
}

func (a mailAdapter) Execute(ctx context.Context, call invoke.Call, p invoke.Prepared) invoke.Result {
	service, err := gmailService(ctx, call)
	if err != nil {
		return invoke.Result{Outcome: "failed", Err: err}
	}
	_, operation, _ := strings.Cut(call.Exec.Binding.Target, "/")
	conn := connectionID(call)
	switch operation {
	case "messages.search":
		list, err := service.Users.Messages.List("me").Q(invoke.String(call.Args, "query")).MaxResults(int64(invoke.Int(call.Args, "max_results", 20))).Context(ctx).Do()
		if err != nil {
			return invoke.Result{Outcome: "failed", Err: classify(err)}
		}
		messages := []map[string]any{}
		for _, item := range list.Messages {
			message, err := service.Users.Messages.Get("me", item.Id).Format("metadata").MetadataHeaders("From", "To", "Cc", "Subject", "Date").Context(ctx).Do()
			if err != nil {
				continue
			}
			messages = append(messages, projectMessage(message, conn, false))
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"account": call.Grant.Connection.Account.Label, "messages": messages,
			"note": "These are snippets. Read a message for its full text."}}
	case "messages.get":
		if threadID := invoke.String(call.Args, "thread_id"); threadID != "" {
			thread, err := service.Users.Threads.Get("me", threadID).Format("full").Context(ctx).Do()
			if err != nil {
				return invoke.Result{Outcome: "failed", Err: classify(err)}
			}
			messages := []map[string]any{}
			for _, message := range thread.Messages {
				messages = append(messages, projectMessage(message, conn, true))
			}
			return invoke.Result{Outcome: "confirmed", Content: map[string]any{"account": call.Grant.Connection.Account.Label, "thread": messages}}
		}
		message, err := service.Users.Messages.Get("me", invoke.String(call.Args, "message_id")).Format("full").Context(ctx).Do()
		if err != nil {
			return invoke.Result{Outcome: "failed", Err: classify(err)}
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"account": call.Grant.Connection.Account.Label, "message": projectMessage(message, conn, true)}}
	case "drafts.create", "drafts.update":
		return saveDraft(ctx, service, call, p, operation)
	case "drafts.send":
		current, err := service.Users.Drafts.Get("me", p.Spec["draft"]).Format("minimal").Context(ctx).Do()
		if err != nil {
			return invoke.Result{Outcome: "failed", Err: classify(err)}
		}
		if current.Message.Id != p.Precondition {
			return invoke.Result{Outcome: "failed", Err: errors.New("the draft changed after the send was prepared; review it again before sending")}
		}
		if a.selfOnly && len(othersThanSelf(call, strings.Split(p.Spec["recipients"], ","))) > 0 {
			// Checked again at send: the prepared recipients are what the approval showed.
			return invoke.Result{Outcome: "failed", Err: errors.New("this Nori sends email only to the connected account's own address"), Cleared: state.ClearedDidNotRun}
		}
		sent, err := service.Users.Drafts.Send("me", &gmail.Draft{Id: p.Spec["draft"]}).Context(ctx).Do()
		if err != nil {
			if definite(err) {
				return invoke.Result{Outcome: "failed", Err: classify(err)}
			}
			return invoke.Result{Outcome: "uncertain", Err: classify(err)}
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"summary": "Sent " + fmt.Sprintf("%q", p.Spec["subject"]) + " to " + p.Spec["recipients"],
			"message_id": sent.Id, "thread_id": sent.ThreadId},
			ProviderRefs: []state.ProviderRef{{ConnectionID: conn, Kind: "mail_message", ProviderID: sent.Id, URL: "https://mail.google.com/mail/u/0/#sent/" + sent.ThreadId}}}
	case "messages.modify":
		add, remove, err := labelIDs(ctx, service, splitNonEmpty(p.Spec["add"]), splitNonEmpty(p.Spec["remove"]))
		if err != nil {
			return invoke.Result{Outcome: "failed", Err: err}
		}
		err = service.Users.Messages.BatchModify("me", &gmail.BatchModifyMessagesRequest{Ids: splitNonEmpty(p.Spec["ids"]), AddLabelIds: add, RemoveLabelIds: remove}).Context(ctx).Do()
		if err != nil {
			if definite(err) {
				return invoke.Result{Outcome: "failed", Err: classify(err)}
			}
			return invoke.Result{Outcome: "uncertain", Err: classify(err)}
		}
		return invoke.Result{Outcome: "confirmed", Content: map[string]any{"summary": p.Summary}, ProviderRefs: p.Resources}
	}
	return invoke.Result{Outcome: "failed", Err: errors.New("unknown mail operation")}
}

func splitNonEmpty(value string) []string {
	out := []string{}
	for _, part := range strings.Split(value, ",") {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

var systemLabels = map[string]bool{"INBOX": true, "UNREAD": true, "STARRED": true, "IMPORTANT": true, "SPAM": true, "TRASH": true}

func labelIDs(ctx context.Context, service *gmail.Service, add, remove []string) ([]string, []string, error) {
	labels, err := service.Users.Labels.List("me").Context(ctx).Do()
	if err != nil {
		return nil, nil, classify(err)
	}
	byName := map[string]string{}
	for _, label := range labels.Labels {
		byName[strings.ToLower(label.Name)] = label.Id
	}
	resolve := func(names []string) ([]string, error) {
		out := []string{}
		for _, name := range names {
			if systemLabels[name] {
				out = append(out, name)
				continue
			}
			id, ok := byName[strings.ToLower(name)]
			if !ok {
				return nil, fmt.Errorf("the label %q does not exist in this account", name)
			}
			out = append(out, id)
		}
		return out, nil
	}
	addIDs, err := resolve(add)
	if err != nil {
		return nil, nil, err
	}
	removeIDs, err := resolve(remove)
	return addIDs, removeIDs, err
}

// draftSubject is the subject the draft will carry: the one given, or for a reply the original's with "Re: ", as
// saveDraft writes it. It reads the original only for the summary; when that fails the summary says it is a reply.
func draftSubject(ctx context.Context, call invoke.Call) string {
	subject := invoke.String(call.Args, "subject")
	reply := invoke.String(call.Args, "reply_to_message_id")
	if subject != "" || reply == "" || call.Grant == nil {
		return subject
	}
	service, err := gmailService(ctx, call)
	if err != nil {
		return ""
	}
	original, err := service.Users.Messages.Get("me", reply).Format("metadata").MetadataHeaders("Subject").Context(ctx).Do()
	if err != nil {
		return ""
	}
	return replySubject(header(original.Payload.Headers, "Subject"), invoke.String(call.Args, "mode"))
}

func replySubject(original, mode string) string {
	if !strings.HasPrefix(strings.ToLower(original), "re:") && mode != "forward" {
		return "Re: " + original
	}
	return original
}

// draftSummary never prints an empty subject or recipient list (frontend saw `Draft "" to  (not sent)`).
func draftSummary(verb, subject string, recipients []string) string {
	title := "(no subject)"
	if subject != "" {
		title = strconv.Quote(subject)
	}
	to := "with no recipients yet"
	if len(recipients) > 0 {
		to = "to " + strings.Join(recipients, ", ")
	}
	return fmt.Sprintf("%s %s %s (not sent)", verb, title, to)
}

func encodeHeader(value string) string { return mime.QEncoding.Encode("utf-8", value) }

func saveDraft(ctx context.Context, service *gmail.Service, call invoke.Call, p invoke.Prepared, operation string) invoke.Result {
	args := call.Args
	subject := invoke.String(args, "subject")
	threadID, inReplyTo, references := "", "", ""
	if reply := invoke.String(args, "reply_to_message_id"); reply != "" {
		original, err := service.Users.Messages.Get("me", reply).Format("metadata").MetadataHeaders("Message-Id", "References", "Subject", "From", "To", "Cc").Context(ctx).Do()
		if err != nil {
			return invoke.Result{Outcome: "failed", Err: classify(err)}
		}
		threadID = original.ThreadId
		inReplyTo = header(original.Payload.Headers, "Message-Id")
		references = strings.TrimSpace(header(original.Payload.Headers, "References") + " " + inReplyTo)
		if subject == "" {
			subject = replySubject(header(original.Payload.Headers, "Subject"), invoke.String(args, "mode"))
		}
	}
	var raw strings.Builder
	fmt.Fprintf(&raw, "From: %s\r\n", call.Grant.Connection.Account.Label)
	for _, name := range []string{"to", "cc", "bcc"} {
		if list := invoke.Strings(args, name); len(list) > 0 {
			fmt.Fprintf(&raw, "%s: %s\r\n", strings.ToUpper(name[:1])+name[1:], strings.Join(list, ", "))
		}
	}
	fmt.Fprintf(&raw, "Subject: %s\r\nMessage-ID: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n", encodeHeader(subject), p.Keys["message_id"])
	if inReplyTo != "" {
		fmt.Fprintf(&raw, "In-Reply-To: %s\r\nReferences: %s\r\n", inReplyTo, references)
	}
	raw.WriteString("\r\n" + invoke.String(args, "body"))
	message := &gmail.Message{Raw: base64.URLEncoding.EncodeToString([]byte(raw.String())), ThreadId: threadID}
	var draft *gmail.Draft
	var err error
	if operation == "drafts.update" {
		draft, err = service.Users.Drafts.Update("me", p.Spec["draft"], &gmail.Draft{Id: p.Spec["draft"], Message: message}).Context(ctx).Do()
	} else {
		draft, err = service.Users.Drafts.Create("me", &gmail.Draft{Message: message}).Context(ctx).Do()
	}
	if err != nil {
		if definite(err) {
			return invoke.Result{Outcome: "failed", Err: classify(err)}
		}
		return invoke.Result{Outcome: "uncertain", Err: classify(err)}
	}
	return draftResult(call, draft, p, subject)
}

// subject is the subject the draft was saved with, which for a reply can differ from the arguments.
func draftResult(call invoke.Call, draft *gmail.Draft, p invoke.Prepared, subject string) invoke.Result {
	conn := connectionID(call)
	return invoke.Result{Outcome: "confirmed", ProviderRefs: []state.ProviderRef{{ConnectionID: conn, Kind: "mail_draft", ProviderID: draft.Id,
		ProviderRevision: draft.Message.Id, URL: "https://mail.google.com/mail/u/0/#drafts"}},
		Content: map[string]any{"summary": "Saved a draft; it has not been sent.", "draft": map[string]any{
			"ref":    map[string]any{"connection_id": conn, "kind": "mail_draft", "provider_id": draft.Id, "provider_revision": draft.Message.Id},
			"sender": map[string]any{"email": call.Grant.Connection.Account.Label}, "to": emails(p.Spec["to"]), "cc": emails(p.Spec["cc"]),
			"bcc": emails(p.Spec["bcc"]), "subject": subject, "body_text": invoke.String(call.Args, "body"),
			"attachment_ids": []string{}, "state": "draft",
		}}}
}

func emails(list string) []map[string]any {
	out := []map[string]any{}
	for _, address := range splitNonEmpty(list) {
		out = append(out, map[string]any{"email": address})
	}
	return out
}

// Reconcile finds an interrupted draft or send by the Message-ID Nori assigned.
func (a mailAdapter) Reconcile(ctx context.Context, call invoke.Call, p invoke.Prepared) (invoke.Result, bool) {
	service, err := gmailService(ctx, call)
	if err != nil {
		return invoke.Result{}, false
	}
	_, operation, _ := strings.Cut(call.Exec.Binding.Target, "/")
	switch operation {
	case "drafts.create":
		drafts, err := service.Users.Drafts.List("me").Q("rfc822msgid:" + strings.Trim(p.Keys["message_id"], "<>")).Context(ctx).Do()
		if err != nil {
			return invoke.Result{}, false
		}
		if len(drafts.Drafts) == 0 {
			return invoke.Result{Outcome: "failed", Err: errors.New("the interrupted attempt did not create the draft")}, true
		}
		draft, err := service.Users.Drafts.Get("me", drafts.Drafts[0].Id).Format("minimal").Context(ctx).Do()
		if err != nil {
			return invoke.Result{}, false
		}
		return draftResult(call, draft, p, draftSubject(ctx, call)), true
	case "drafts.send":
		id := strings.Trim(p.Spec["message_id"], "<>")
		if id == "" {
			return invoke.Result{}, false
		}
		found, err := service.Users.Messages.List("me").Q("in:sent rfc822msgid:" + id).Context(ctx).Do()
		if err != nil {
			return invoke.Result{}, false
		}
		if len(found.Messages) > 0 {
			return invoke.Result{Outcome: "confirmed", Content: map[string]any{"summary": "The message was sent.", "message_id": found.Messages[0].Id},
				ProviderRefs: []state.ProviderRef{{ConnectionID: connectionID(call), Kind: "mail_message", ProviderID: found.Messages[0].Id}}}, true
		}
		if _, err = service.Users.Drafts.Get("me", p.Spec["draft"]).Context(ctx).Do(); err == nil {
			return invoke.Result{Outcome: "failed", Err: errors.New("the draft is still unsent")}, true
		}
	}
	return invoke.Result{}, false
}

type contactsAdapter struct{}

func (contactsAdapter) Prepare(_ context.Context, call invoke.Call) (invoke.Prepared, error) {
	return invoke.Prepared{Summary: "Look up contacts: " + invoke.String(call.Args, "query"), Effect: extensions.EffectRead}, nil
}

func (contactsAdapter) Execute(ctx context.Context, call invoke.Call, _ invoke.Prepared) invoke.Result {
	if call.Grant == nil {
		return invoke.Result{Outcome: "failed", Err: errors.New("contacts need a connected account")}
	}
	service, err := people.NewService(ctx, option.WithHTTPClient(call.Grant.Client))
	if err != nil {
		return invoke.Result{Outcome: "failed", Err: err}
	}
	query := invoke.String(call.Args, "query")
	conn := connectionID(call)
	candidates := []map[string]any{}
	add := func(person *people.Person, hint string) {
		var emailsFound []string
		for _, e := range person.EmailAddresses {
			emailsFound = append(emailsFound, e.Value)
		}
		if len(emailsFound) == 0 {
			return
		}
		name := emailsFound[0]
		if len(person.Names) > 0 {
			name = person.Names[0].DisplayName
		}
		candidates = append(candidates, map[string]any{"source": map[string]any{"connection_id": conn, "kind": "contact", "provider_id": person.ResourceName},
			"name": name, "emails": emailsFound, "hint": hint})
	}
	contacts, err := service.People.SearchContacts().Query(query).ReadMask("names,emailAddresses").PageSize(10).Context(ctx).Do()
	if err != nil {
		return invoke.Result{Outcome: "failed", Err: classify(err)}
	}
	for _, result := range contacts.Results {
		add(result.Person, "saved contact")
	}
	if other, err := service.OtherContacts.Search().Query(query).ReadMask("names,emailAddresses").PageSize(10).Context(ctx).Do(); err == nil {
		for _, result := range other.Results {
			add(result.Person, "previously emailed")
		}
	}
	note := "Confirm with the user when more than one candidate could be meant."
	return invoke.Result{Outcome: "confirmed", Content: map[string]any{"candidates": candidates, "note": note}}
}
