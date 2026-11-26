package homeconnector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestOllamaStructuredOutputFallback(t *testing.T) {
	var mu sync.Mutex
	var requests []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"test:local","digest":"local-digest"},{"name":"remote:cloud","digest":"remote","remote_model":"remote"},{"name":"aliased:latest","digest":"remote2","remote_host":"https://example.com"}]}`)
		case "/api/show":
			fmt.Fprint(w, `{"capabilities":["completion","tools"]}`)
		case "/v1/chat/completions":
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			requests = append(requests, request)
			attempt := len(requests)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if attempt == 1 {
				w.WriteHeader(http.StatusNotImplemented)
				fmt.Fprint(w, `{"error":"structured output is unavailable"}`)
				return
			}
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	ollama := NewOllama(server.URL)
	models, err := ollama.Inventory(context.Background())
	if err != nil || len(models) != 1 || models[0].Name != "test:local" {
		t.Fatalf("inventory included a remote model or failed: %v %+v", err, models)
	}
	request := `{"model":"test:local","messages":[{"role":"user","content":"hello"}],"max_completion_tokens":300,"store":false,"response_format":{"type":"json_schema","json_schema":{"schema":{"type":"object","required":["text"]}}}}`
	response, code := ollama.Open(context.Background(), "chat", "test:local", "local-digest", []byte(request))
	if code != "" {
		t.Fatal(code)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status %d", response.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("expected one bounded retry, got %d", len(requests))
	}
	if _, ok := requests[0]["response_format"]; !ok {
		t.Fatal("first request lost schema")
	}
	if _, ok := requests[1]["response_format"]; ok {
		t.Fatal("retry kept unsupported response format")
	}
	if requests[1]["max_tokens"] != float64(300) {
		t.Fatal("retry lost output limit")
	}
	messages, ok := requests[1]["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatal("retry lost conversation")
	}
	instruction, ok := messages[1].(map[string]any)
	if !ok || instruction["role"] != "system" || !strings.Contains(instruction["content"].(string), `"required":["text"]`) {
		t.Fatalf("retry lost schema instruction: %+v", instruction)
	}
}

func TestOllamaInputBounds(t *testing.T) {
	cases := []struct {
		operation string
		body      string
		valid     bool
	}{
		{"chat", `{"model":"m","messages":[],"max_completion_tokens":300}`, true},
		{"chat", `{"model":"m","messages":[]}`, false},
		{"responses", `{"model":"m","input":[],"max_output_tokens":1024}`, true},
		{"responses", `{"model":"m","input":[],"max_output_tokens":40000}`, false},
		{"responses", `{"model":"m","input":[],"max_output_tokens":100,"tools":[{"type":"web_search"}]}`, false},
		{"chat", `{"model":"m","messages":[{"type":"image_url","image_url":{"url":"http://localhost:1234"}}],"max_tokens":20}`, false},
		{"responses", `{"model":"m","input":[{"type":"input_file","file_url":"http://localhost:1234"}],"max_output_tokens":20}`, false},
		{"responses", `{"model":"m","input":[],"max_output_tokens":100,"tools":[{"type":"function","parameters":{"properties":{"file_url":{"type":"string"}}}}]}`, true},
	}
	for _, tc := range cases {
		var body map[string]any
		decoder := json.NewDecoder(strings.NewReader(tc.body))
		decoder.UseNumber()
		if err := decoder.Decode(&body); err != nil {
			t.Fatal(err)
		}
		if got := validProviderInput(body, tc.operation); got != tc.valid {
			t.Errorf("%s %s valid=%v, want %v", tc.operation, tc.body, got, tc.valid)
		}
	}
}
