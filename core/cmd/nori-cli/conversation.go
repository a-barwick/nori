package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"nori/internal/sse"
)

type message struct {
	ID     string `json:"id"`
	TurnID string `json:"turnId,omitempty"`
	Role   string `json:"role"`
	Text   string `json:"text"`
	Status string `json:"status"`
}

type conversationView struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Mode     string    `json:"mode"`
	Position int64     `json:"position"`
	Messages []message `json:"messages,omitempty"`
}

type event struct {
	Seq          int64            `json:"seq"`
	Kind         string           `json:"kind"`
	Conversation conversationView `json:"conversation"`
	Messages     []message        `json:"messages"`
}

func conversationPath(id string) string { return "/v1/conversations/" + url.PathEscape(id) }

func (a *cli) converse(ctx context.Context, command string, args []string) error {
	flags := a.flags(command)
	id := flags.String("conversation", "", "conversation ID or assistant")
	request := flags.String("request-id", "", "stable request UUID")
	var attachments []string
	flags.Func("attach", "file to upload (repeatable)", func(path string) error {
		attachments = append(attachments, path)
		return nil
	})
	if err := flags.Parse(args); err != nil {
		return err
	}
	if command == "ask" {
		text := strings.TrimSpace(strings.Join(flags.Args(), " "))
		if text == "" && len(attachments) == 0 {
			return errors.New("ask requires text or --attach")
		}
		_, err := a.send(ctx, *id, *request, text, attachments)
		return err
	}
	if flags.NArg() != 0 || *request != "" || len(attachments) != 0 {
		return errors.New("chat accepts --conversation only")
	}
	reader := bufio.NewScanner(a.in)
	reader.Buffer(make([]byte, 4096), 1<<20)
	for ctx.Err() == nil {
		if !a.machine {
			fmt.Fprint(a.err, "You › ")
		}
		if !reader.Scan() {
			return reader.Err()
		}
		text := strings.TrimSpace(reader.Text())
		if text == "/exit" || text == "/quit" {
			return nil
		}
		if text == "" {
			continue
		}
		conversation, err := a.send(ctx, *id, "", text, nil)
		if err == nil && conversation != "" {
			*id = conversation
		}
		if err != nil {
			fmt.Fprintln(a.err, err)
		}
	}
	return ctx.Err()
}

func (a *cli) send(parent context.Context, conversationID, explicit, text string, paths []string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, a.timeout)
	defer cancel()
	files, err := readFiles(paths)
	if err != nil {
		return conversationID, err
	}
	identity := []string{"send", conversationID, text}
	for _, file := range files {
		identity = append(identity, file.name, file.digest)
	}
	requestID, done, err := journalID(explicit, a.scope(), identity)
	if err != nil {
		return conversationID, err
	}
	var view conversationView
	if conversationID == "" {
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte("nori-cli-chat:"+requestID)).String()
		if err := a.client.call(ctx, "POST", "/v1/conversations", map[string]string{"id": id}, &view); err != nil {
			return "", err
		}
		conversationID = view.ID
	} else if err := a.client.call(ctx, "GET", conversationPath(conversationID), nil, &view); err != nil {
		return conversationID, err
	}
	uploads := make([]string, 0, len(files))
	for i, file := range files {
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("nori-cli-upload:%s:%d", requestID, i))).String()
		if err := a.upload(ctx, conversationID, id, file, nil); err != nil {
			return conversationID, err
		}
		uploads = append(uploads, id)
	}
	input := struct {
		ID          string   `json:"id"`
		Text        string   `json:"text"`
		Attachments []string `json:"attachments,omitempty"`
	}{requestID, text, uploads}
	path := conversationPath(conversationID)
	if err := a.client.call(ctx, "POST", path+"/messages", input, nil); err != nil {
		return conversationID, err
	}
	reply := replyOutput{cli: a, conversationID: conversationID, requestID: requestID}
	final, err := a.waitReply(ctx, view, &reply)
	if err != nil {
		return conversationID, fmt.Errorf("%w. Repeat the same send to resume request %s", err, requestID)
	}
	done()
	if final.Status != "completed" {
		return conversationID, fmt.Errorf("reply %s", final.Status)
	}
	return conversationID, nil
}

type replyOutput struct {
	*cli
	conversationID string
	requestID      string
	printed        string
}

func (r *replyOutput) show(reply message) error {
	if r.machine {
		if reply.Status == "streaming" {
			return nil
		}
		return r.emit(struct {
			ConversationID string  `json:"conversation_id"`
			RequestID      string  `json:"request_id"`
			Reply          message `json:"reply"`
		}{r.conversationID, r.requestID, reply})
	}
	delta, extends := strings.CutPrefix(reply.Text, r.printed)
	if !extends {
		if reply.Status == "streaming" {
			return nil
		}
		delta = "\n" + reply.Text
	}
	r.printed = reply.Text
	if reply.Status != "streaming" {
		delta += "\n"
	}
	_, err := fmt.Fprint(r.out, delta)
	return err
}

func findReply(messages []message, inputID string) (message, bool) {
	turn := inputID
	for _, msg := range messages {
		if msg.ID == inputID+"-user" && msg.TurnID != "" {
			turn = msg.TurnID
			break
		}
	}
	for _, msg := range messages {
		if msg.Role == "assistant" && (msg.ID == turn+"-assistant" || msg.TurnID == turn) {
			return msg, true
		}
	}
	return message{}, false
}

func (a *cli) waitReply(ctx context.Context, initial conversationView, out *replyOutput) (message, error) {
	after := initial.Position
	path := conversationPath(initial.ID)
	reload := func() (message, bool, error) {
		var view conversationView
		if err := a.client.call(ctx, "GET", path, nil, &view); err != nil {
			return message{}, false, err
		}
		after = view.Position
		reply, ok := findReply(view.Messages, out.requestID)
		if !ok {
			return message{}, false, nil
		}
		return reply, reply.Status != "streaming", out.show(reply)
	}
	if reply, ok := findReply(initial.Messages, out.requestID); ok && reply.Status != "streaming" {
		return reply, out.show(reply)
	}
	// On a retry, the input or reply can already be present at the starting cursor.
	if slices.ContainsFunc(initial.Messages, func(m message) bool { return m.ID == out.requestID+"-user" }) {
		if reply, complete, err := reload(); complete || err != nil {
			return reply, err
		}
	}
	for ctx.Err() == nil {
		response, err := a.client.do(ctx, "GET", path+"/events?after="+strconv.FormatInt(after, 10), "", nil)
		if err != nil {
			if isStatus(err, http.StatusBadRequest) {
				if reply, complete, err := reload(); complete || err != nil {
					return reply, err
				}
			} else if !retryable(err) {
				return message{}, err
			}
			if err := pause(ctx, 400*time.Millisecond); err != nil {
				return message{}, err
			}
			continue
		}
		reply, complete, reset, err := a.follow(response, &after, out)
		if complete || err != nil {
			return reply, err
		}
		if reset {
			if reply, complete, err := reload(); complete || err != nil {
				return reply, err
			}
		} else if err := pause(ctx, 400*time.Millisecond); err != nil {
			return message{}, err
		}
	}
	return message{}, ctx.Err()
}

func (a *cli) follow(response *http.Response, after *int64, out *replyOutput) (reply message, complete, reset bool, err error) {
	defer response.Body.Close()
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return reply, false, false, errors.New("Nori did not return an event stream")
	}
	reader := sse.NewReader(response.Body)
	for {
		frame, err := reader.Next()
		if err != nil {
			return reply, false, false, nil
		}
		var event event
		if json.Unmarshal(frame.Data, &event) != nil || event.Conversation.ID != out.conversationID || frame.ID != strconv.FormatInt(event.Seq, 10) {
			return reply, false, true, nil
		}
		if event.Kind == "reset" {
			return reply, false, true, nil
		}
		if event.Seq <= *after {
			continue
		}
		if event.Seq != *after+1 {
			return reply, false, true, nil
		}
		*after = event.Seq
		switch event.Kind {
		case "reply.updated", "turn.completed", "turn.failed", "turn.cancelled", "message":
			msg, ok := findReply(event.Messages, out.requestID)
			if ok {
				if err := out.show(msg); err != nil || msg.Status != "streaming" {
					return msg, msg.Status != "streaming", false, err
				}
			} else if out.conversationID == "assistant" && slices.ContainsFunc(event.Messages, func(m message) bool { return m.Role == "assistant" }) {
				return reply, false, true, nil
			}
		}
	}
}
