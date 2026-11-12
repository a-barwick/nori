package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"nori/internal/sse"
)

const conversationSchema = "conversations.schema.json#/$defs/Conversation"

func checkConversationResume(base string, compile func(string) (*jsonschema.Schema, error)) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := &http.Client{}
	root := strings.TrimRight(base, "/") + "/users/44444444-4444-4444-8444-444444444444/v1/conversations"
	request := func(method, path string, body []byte, status int, schema string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, method, root+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != status {
			return nil, fmt.Errorf("%s %s returned %d, want %d", method, path, res.StatusCode, status)
		}
		data, err := io.ReadAll(res.Body)
		if err == nil {
			err = validateBytes(compile, conversationSchema+schema, data)
		}
		return data, err
	}

	type message struct{ ID, Role, Text, Status string }
	type view struct {
		ID       string
		Position int64
		Messages []message
	}
	var chat view
	data, err := request("POST", "", []byte(`{"title":"Stream resume check"}`), 201, "View")
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, &chat); err != nil {
		return err
	}
	defer request("DELETE", "/"+chat.ID, nil, 200, "Deleted")
	after := chat.Position
	open := func(id string, after int64, header bool) (*http.Response, error) {
		path := root + "/" + id + "/events"
		if !header {
			path += "?after=" + strconv.FormatInt(after, 10)
		}
		req, err := http.NewRequestWithContext(ctx, "GET", path, nil)
		if err != nil {
			return nil, err
		}
		if header {
			req.Header.Set("Last-Event-ID", strconv.FormatInt(after, 10))
		}
		res, err := client.Do(req)
		if err == nil && (res.StatusCode != 200 || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream")) {
			res.Body.Close()
			return nil, fmt.Errorf("stream returned %d", res.StatusCode)
		}
		return res, err
	}

	type event struct {
		Seq          int64
		Kind         string
		Conversation view
		Messages     []message
	}
	next := func(reader *sse.Reader, id string, after *int64) (event, error) {
		var e event
		frame, err := reader.Next()
		if err == nil {
			err = validateBytes(compile, conversationSchema+"Event", frame.Data)
		}
		if err == nil {
			err = json.Unmarshal(frame.Data, &e)
		}
		if err != nil {
			return e, err
		}
		if e.Conversation.ID != id || e.Seq != *after+1 || strconv.FormatInt(e.Seq, 10) != frame.ID {
			return e, fmt.Errorf("sequence or conversation mismatch at %d after %d", e.Seq, *after)
		}
		*after = e.Seq
		fmt.Printf("  %s sequence %d %s\n", id, e.Seq, e.Kind)
		return e, nil
	}

	first, err := open(chat.ID, after, false)
	if err != nil {
		return err
	}
	defer first.Body.Close()
	body := fmt.Appendf(nil, `{"id":%q,"text":"Replay stream resume check."}`, newUUID())
	if _, err := request("POST", "/"+chat.ID+"/messages", body, 202, "Accepted"); err != nil {
		return err
	}
	reader := sse.NewReader(first.Body)
	cut := false
	for !cut {
		e, err := next(reader, chat.ID, &after)
		if err != nil {
			return err
		}
		for _, message := range e.Messages {
			cut = cut || message.Role == "assistant" && message.Status == "streaming" && message.Text != ""
		}
		if e.Kind == "turn.completed" || e.Kind == "turn.failed" {
			return fmt.Errorf("reply ended before the planned mid-reply disconnect")
		}
	}
	first.Body.Close()
	fmt.Printf("  dropped the stream after sequence %d\n", after)
	second, err := open(chat.ID, after, true)
	if err != nil {
		return err
	}
	defer second.Body.Close()
	reader = sse.NewReader(second.Body)
	for {
		e, err := next(reader, chat.ID, &after)
		if err != nil {
			return err
		}
		if e.Kind == "turn.failed" || e.Kind == "turn.cancelled" {
			return fmt.Errorf("recorded chat did not complete")
		}
		if e.Kind == "turn.completed" {
			break
		}
	}
	if _, err := request("GET", "/"+chat.ID+"/messages", nil, 200, "History"); err != nil {
		return err
	}

	data, err = request("GET", "/assistant", nil, 200, "View")
	if err != nil {
		return err
	}
	var assistant view
	if err := json.Unmarshal(data, &assistant); err != nil {
		return err
	}
	after = assistant.Position
	stream, err := open("assistant", after, false)
	if err != nil {
		return err
	}
	defer stream.Body.Close()
	const prompt = "Replay assistant contract probe."
	body = fmt.Appendf(nil, `{"id":%q,"text":%q}`, newUUID(), prompt)
	if _, err := request("POST", "/assistant/messages", body, 202, "Accepted"); err != nil {
		return err
	}
	reader = sse.NewReader(stream.Body)
	sawInput := false
	for {
		e, err := next(reader, "assistant", &after)
		if err != nil {
			return err
		}
		for _, message := range e.Messages {
			if message.Role == "user" && message.Text == prompt {
				sawInput = true
			}
			if sawInput && message.Role == "assistant" && message.Status == "completed" {
				_, err := request("GET", "/assistant/messages", nil, 200, "History")
				return err
			}
		}
	}
}
