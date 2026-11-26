package homeconnector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"nori/internal/homeaiwire"
)

func TestSlowHealthRefreshDoesNotBlockReady(t *testing.T) {
	var inventoryCalls atomic.Int32
	healthStarted := make(chan struct{})
	releaseHealth := make(chan struct{})
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			if inventoryCalls.Add(1) == 2 {
				close(healthStarted)
				select {
				case <-releaseHealth:
				case <-r.Context().Done():
					return
				}
			}
			fmt.Fprint(w, `{"models":[]}`)
		default:
			t.Errorf("unexpected Ollama endpoint %s", r.URL.Path)
		}
	}))
	defer ollama.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{homeaiwire.Subprotocol}})
		if err != nil {
			done <- err
			return
		}
		defer conn.CloseNow()
		var message homeaiwire.Message
		if err := wsjson.Read(ctx, conn, &message); err != nil || message.Type != "hello" {
			done <- fmt.Errorf("receive hello: %v", err)
			return
		}
		reader := make(chan homeaiwire.Message, 4)
		go func() {
			for {
				var item homeaiwire.Message
				if wsjson.Read(ctx, conn, &item) != nil {
					return
				}
				reader <- item
			}
		}()
		select {
		case <-healthStarted:
		case <-ctx.Done():
			done <- ctx.Err()
			return
		}
		if err := wsjson.Write(ctx, conn, homeaiwire.Message{Type: "infer", RequestID: "r", AttemptID: "a"}); err != nil {
			done <- err
			return
		}
		for _, want := range []string{"failed", "ready"} {
			select {
			case item := <-reader:
				if item.Type != want {
					done <- fmt.Errorf("got %s, want %s", item.Type, want)
					return
				}
			case <-ctx.Done():
				done <- ctx.Err()
				return
			}
		}
		done <- nil
	}))
	defer relay.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	connector := &Connector{
		config:         Config{RelayURL: relay.URL, StateDir: dir},
		ollama:         NewOllama(ollama.URL),
		healthInterval: 10 * time.Millisecond,
	}
	sessionDone := make(chan error, 1)
	go func() {
		sessionDone <- connector.session(ctx, Credential{AccessToken: "test", Expiry: time.Now().Add(time.Hour)})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancel()
	close(releaseHealth)
	<-sessionDone
}

func TestSessionRolloverWaitsForActiveInference(t *testing.T) {
	allowResponse := make(chan struct{})
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"model:local","digest":"model-digest"}]}`)
		case "/api/show":
			fmt.Fprint(w, `{"capabilities":["completion"]}`)
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			select {
			case <-allowResponse:
				fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
			case <-r.Context().Done():
			}
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
	}))
	defer ollama.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	relayDone := make(chan error, 1)
	drained := make(chan struct{})
	release := make(chan struct{})
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{homeaiwire.Subprotocol}})
		if err != nil {
			relayDone <- err
			return
		}
		defer conn.CloseNow()
		var message homeaiwire.Message
		if err := wsjson.Read(ctx, conn, &message); err != nil || message.Type != "hello" {
			relayDone <- fmt.Errorf("receive hello: %v", err)
			return
		}
		request := homeaiwire.Message{
			Type: "infer", RequestID: "request", AttemptID: "attempt", Operation: "chat",
			Model: "model:local", Digest: "model-digest", Deadline: time.Now().Add(time.Minute),
			Payload: []byte(`{"model":"model:local","messages":[{"role":"user","content":"hello"}],"max_tokens":100}`),
		}
		if err := wsjson.Write(ctx, conn, request); err != nil {
			relayDone <- err
			return
		}
		for _, want := range []string{"accepted", "headers", "draining"} {
			if err := wsjson.Read(ctx, conn, &message); err != nil || message.Type != want {
				relayDone <- fmt.Errorf("got %s, want %s: %v", message.Type, want, err)
				return
			}
		}
		if err := wsjson.Write(ctx, conn, homeaiwire.Message{Type: "drained"}); err != nil {
			relayDone <- err
			return
		}
		close(drained)
		select {
		case <-release:
		case <-ctx.Done():
			relayDone <- ctx.Err()
			return
		}
		for _, want := range []string{"chunk", "complete", "ready"} {
			if err := wsjson.Read(ctx, conn, &message); err != nil || message.Type != want {
				relayDone <- fmt.Errorf("got %s, want %s: %v", message.Type, want, err)
				return
			}
		}
		_ = conn.Close(websocket.StatusNormalClosure, "done")
		relayDone <- nil
	}))
	defer relay.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	connector := &Connector{
		config:         Config{RelayURL: relay.URL, StateDir: dir},
		ollama:         NewOllama(ollama.URL),
		healthInterval: time.Hour,
		rolloverLead:   2 * time.Second,
	}
	sessionDone := make(chan error, 1)
	go func() {
		sessionDone <- connector.session(ctx, Credential{AccessToken: "test", Expiry: time.Now().Add(2 * time.Second)})
	}()
	select {
	case <-drained:
	case err := <-relayDone:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-sessionDone:
		t.Fatalf("session closed during active inference: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(allowResponse)
	close(release)
	select {
	case err := <-relayDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := <-sessionDone; !errors.Is(err, errSessionRollover) {
		t.Fatalf("expected rollover after inference, got %v", err)
	}
}
