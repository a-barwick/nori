package httpjson

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVertexResponse(t *testing.T) {
	for _, status := range []int{201, 202, 204, 401, 403, 409, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			body := `{"code":"test"}`
			if status == 204 {
				body = ""
			}
			ServeVertexResponse(recorder, func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "3")
				w.Header().Set("Set-Cookie", "private=test")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, body)
			})
			response := recorder.Result()
			defer response.Body.Close()
			if response.StatusCode != 200 || !ReadVertexResponse(response) {
				t.Fatal("response could not be restored")
			}
			if response.StatusCode != status || response.Header.Get("Retry-After") != "3" || response.Header.Get("Set-Cookie") != "" {
				t.Fatalf("unexpected response status or headers: %d %v", response.StatusCode, response.Header)
			}
			data, err := io.ReadAll(response.Body)
			if err != nil || string(data) != body {
				t.Fatalf("read %q, error %v", data, err)
			}
		})
	}
}

func TestVertexEventStream(t *testing.T) {
	recorder := httptest.NewRecorder()
	ServeVertexResponse(recorder, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": ready\n\n")
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Fatal(err)
		}
	})
	response := recorder.Result()
	defer response.Body.Close()
	if !ReadVertexResponse(response) || !recorder.Flushed || recorder.Body.String() != ": ready\n\n" {
		t.Fatal("event stream was changed")
	}
}
