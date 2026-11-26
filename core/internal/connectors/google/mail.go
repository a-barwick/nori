package google

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
	"unicode/utf8"

	"nori/internal/connections"
	"nori/internal/extensions"
	"nori/internal/invoke"
	"nori/internal/state"
)

const (
	gmailSendURL           = "https://gmail.googleapis.com/gmail/v1/users/me/messages/send"
	maxMailAttachmentBytes = 18 << 20
)

type mailAdapter struct{}

type mailAttachment struct {
	ID, Name, MediaType, SHA256 string
	Size                        int
}

type mailMessage struct {
	From        string
	To, CC, BCC []string
	Subject     string
	Body        string
	Attachments []mailAttachment
}

func mailAddress(value string) (string, error) {
	address, err := mail.ParseAddress(value)
	if err != nil || len(address.Address) > 254 || strings.ContainsAny(address.Address, "\r\n\x00") {
		return "", fmt.Errorf("%w: enter a valid email address", invoke.ErrInvalidArguments)
	}
	return address.Address, nil
}

func mailRecipients(args map[string]any, field string) ([]string, error) {
	values := invoke.Strings(args, field)
	addresses := make([]string, 0, len(values))
	for _, value := range values {
		address, err := mailAddress(value)
		if err != nil {
			return nil, err
		}
		addresses = append(addresses, address)
	}
	return addresses, nil
}

func loadMailAttachment(ctx context.Context, call invoke.Call, id string) (mailAttachment, []byte, error) {
	if call.Upload == nil || !state.ValidUploadID(id) {
		return mailAttachment{}, nil, fmt.Errorf("%w: attachments must be files provided to this task", invoke.ErrInvalidArguments)
	}
	name, rawMediaType, data, err := call.Upload(ctx, id)
	if err != nil {
		return mailAttachment{}, nil, fmt.Errorf("%w: an attachment is unavailable in this task", invoke.ErrInvalidArguments)
	}
	mediaType, _, err := mime.ParseMediaType(rawMediaType)
	if err != nil || name == "" || strings.ContainsAny(name, "\r\n\x00") || len(data) > state.MaxUploadBytes {
		return mailAttachment{}, nil, fmt.Errorf("%w: the attachment has an invalid name, media type, or size", invoke.ErrInvalidArguments)
	}
	hash := sha256.Sum256(data)
	return mailAttachment{ID: id, Name: name, MediaType: mediaType, SHA256: hex.EncodeToString(hash[:]), Size: len(data)}, data, nil
}

func (mailAdapter) Prepare(ctx context.Context, call invoke.Call) (invoke.Prepared, error) {
	if call.Grant == nil {
		return invoke.Prepared{}, errors.New("a connected Google account is required")
	}
	from, err := mailAddress(call.Grant.Connection.Account.Label)
	if err != nil {
		return invoke.Prepared{}, err
	}
	to, err := mailRecipients(call.Args, "to")
	if err != nil || len(to) == 0 {
		return invoke.Prepared{}, fmt.Errorf("%w: add a valid recipient", invoke.ErrInvalidArguments)
	}
	cc, err := mailRecipients(call.Args, "cc")
	if err != nil {
		return invoke.Prepared{}, err
	}
	bcc, err := mailRecipients(call.Args, "bcc")
	if err != nil {
		return invoke.Prepared{}, err
	}
	subject, subjectOK := call.Args["subject"].(string)
	body, bodyOK := call.Args["body"].(string)
	if !subjectOK || strings.ContainsAny(subject, "\r\n\x00") || !bodyOK || utf8.RuneCountInString(body) > 12_000 {
		return invoke.Prepared{}, fmt.Errorf("%w: provide a valid subject and a message of up to 12,000 characters", invoke.ErrInvalidArguments)
	}
	for _, word := range strings.Fields(mime.QEncoding.Encode("UTF-8", subject)) {
		if len(word) > 980 {
			return invoke.Prepared{}, fmt.Errorf("%w: the email subject has an overlong word", invoke.ErrInvalidArguments)
		}
	}
	msg := mailMessage{From: from, To: to, CC: cc, BCC: bcc, Subject: subject, Body: body}
	var total int
	var details []string
	seen := map[string]bool{}
	for _, id := range invoke.Strings(call.Args, "attachments") {
		if seen[id] {
			return invoke.Prepared{}, fmt.Errorf("%w: include each attachment once", invoke.ErrInvalidArguments)
		}
		seen[id] = true
		attachment, _, err := loadMailAttachment(ctx, call, id)
		if err != nil {
			return invoke.Prepared{}, err
		}
		total += attachment.Size
		if total > maxMailAttachmentBytes {
			return invoke.Prepared{}, fmt.Errorf("%w: message attachments must total 18 MB or less", invoke.ErrInvalidArguments)
		}
		msg.Attachments = append(msg.Attachments, attachment)
		details = append(details, "Attachment: "+attachment.Name)
	}
	var preview strings.Builder
	preview.WriteString("To: " + strings.Join(to, ", ") + "\n")
	if len(cc) > 0 {
		preview.WriteString("Cc: " + strings.Join(cc, ", ") + "\n")
	}
	if len(bcc) > 0 {
		preview.WriteString("Bcc: " + strings.Join(bcc, ", ") + "\n")
	}
	preview.WriteString("Subject: " + subject + "\n\n" + body)
	recipients := append(append(append([]string{}, to...), cc...), bcc...)
	spec, err := json.Marshal(msg)
	if err != nil {
		return invoke.Prepared{}, err
	}
	return invoke.Prepared{Summary: preview.String(), PromptTitle: "Send this email?", Effect: extensions.EffectSensitiveSend,
		Recipients: recipients, MaterialEffects: details, Spec: map[string]string{"message": string(spec)}, RequireExplicitApproval: true}, nil
}

func emailHeaderValue(addresses []string) string {
	out := make([]string, 0, len(addresses))
	for _, address := range addresses {
		out = append(out, (&mail.Address{Address: address}).String())
	}
	return strings.Join(out, ",\r\n ")
}

func foldedSubject(subject string) string {
	encoded := mime.QEncoding.Encode("UTF-8", subject)
	var out strings.Builder
	width := 990 - len("Subject: ")
	for len(encoded) > width {
		cut := strings.LastIndexByte(encoded[:width], ' ')
		if cut <= 0 {
			break
		}
		out.WriteString(encoded[:cut])
		out.WriteString("\r\n")
		encoded = encoded[cut:]
		width = 990
	}
	out.WriteString(encoded)
	return out.String()
}

func writeQuotedText(w io.Writer, body string) error {
	writer := quotedprintable.NewWriter(w)
	if _, err := io.WriteString(writer, body); err != nil {
		return err
	}
	return writer.Close()
}

func writeAttachment(w io.Writer, data []byte) error {
	var line [76 + 2]byte
	for len(data) > 0 {
		n := min(len(data), 57)
		width := base64.StdEncoding.EncodedLen(n)
		base64.StdEncoding.Encode(line[:width], data[:n])
		line[width], line[width+1] = '\r', '\n'
		if _, err := w.Write(line[:width+2]); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func encodeMail(ctx context.Context, call invoke.Call, msg mailMessage) ([]byte, error) {
	var out bytes.Buffer
	fmt.Fprintf(&out, "From: %s\r\nTo: %s\r\n", emailHeaderValue([]string{msg.From}), emailHeaderValue(msg.To))
	if len(msg.CC) > 0 {
		fmt.Fprintf(&out, "Cc: %s\r\n", emailHeaderValue(msg.CC))
	}
	if len(msg.BCC) > 0 {
		fmt.Fprintf(&out, "Bcc: %s\r\n", emailHeaderValue(msg.BCC))
	}
	fmt.Fprintf(&out, "Subject: %s\r\nMIME-Version: 1.0\r\n", foldedSubject(msg.Subject))
	const textType = "text/plain; charset=UTF-8"
	if len(msg.Attachments) == 0 {
		fmt.Fprintf(&out, "Content-Type: %s\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n", textType)
		if err := writeQuotedText(&out, msg.Body); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	}
	writer := multipart.NewWriter(&out)
	fmt.Fprintf(&out, "Content-Type: %s\r\n\r\n", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": writer.Boundary()}))
	textPart, err := writer.CreatePart(textproto.MIMEHeader{"Content-Type": {textType}, "Content-Transfer-Encoding": {"quoted-printable"}})
	if err != nil {
		return nil, err
	}
	if err := writeQuotedText(textPart, msg.Body); err != nil {
		return nil, err
	}
	for _, approved := range msg.Attachments {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		attachment, data, err := loadMailAttachment(ctx, call, approved.ID)
		if err != nil {
			return nil, err
		}
		if attachment != approved {
			return nil, fmt.Errorf("%w: an attachment changed after approval", invoke.ErrInvalidArguments)
		}
		part, err := writer.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {approved.MediaType},
			"Content-Transfer-Encoding": {"base64"},
			"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": approved.Name})},
		})
		if err != nil {
			return nil, err
		}
		if err := writeAttachment(part, data); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (mailAdapter) Execute(ctx context.Context, call invoke.Call, prepared invoke.Prepared) invoke.Result {
	client, err := clientFor(call)
	if err != nil {
		return invoke.Result{Outcome: "failed", Err: err, Cleared: state.ClearedDidNotRun}
	}
	var msg mailMessage
	if err := json.Unmarshal([]byte(prepared.Spec["message"]), &msg); err != nil {
		return invoke.Result{Outcome: "failed", Err: err, Cleared: state.ClearedDidNotRun}
	}
	if !strings.EqualFold(msg.From, call.Grant.Connection.Account.Label) {
		return invoke.Result{Outcome: "failed", Err: errors.New("the sending account changed after approval"), Cleared: state.ClearedDidNotRun}
	}
	mimeMessage, err := encodeMail(ctx, call, msg)
	if err != nil {
		return invoke.Result{Outcome: "failed", Err: err, Cleared: state.ClearedDidNotRun}
	}
	var sent struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
	}
	payload := map[string]string{"raw": base64.RawURLEncoding.EncodeToString(mimeMessage)}
	if err := jsonRequest(ctx, client, "POST", gmailSendURL, payload, nil, &sent); err != nil {
		if definite(err) || errors.Is(err, connections.ErrUnauthorized) || errors.Is(err, connections.ErrInsufficientScope) {
			return invoke.Result{Outcome: "failed", Err: err, Cleared: state.ClearedDidNotRun}
		}
		return invoke.Result{Outcome: "uncertain", Err: err}
	}
	if sent.ID == "" {
		return invoke.Result{Outcome: "uncertain", Err: errors.New("Gmail did not return a message receipt")}
	}
	return invoke.Result{Outcome: "confirmed", Content: map[string]any{"message_id": sent.ID, "thread_id": sent.ThreadID, "sent": true},
		ProviderRefs: []state.ProviderRef{{ConnectionID: connectionID(call), Kind: "gmail_message", ProviderID: sent.ID}}}
}
