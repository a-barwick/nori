package google

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type uploadHTTPFixture func(*http.Request) (*http.Response, error)

func (f uploadHTTPFixture) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestLargeDriveUploadUsesAGoogleSession(t *testing.T) {
	const session = "https://www.googleapis.com/upload/drive/v3/files/upload-target?uploadType=resumable&upload_id=test-session"
	const endpoint = "https://www.googleapis.com/upload/drive/v3/files/upload-target?fields=id,name"
	content := bytes.Repeat([]byte("a"), 5_000_001)
	for _, location := range []string{session, "https://example.test/upload/drive/v3/files?upload_id=other"} {
		calls := 0
		client := &http.Client{Transport: uploadHTTPFixture(func(req *http.Request) (*http.Response, error) {
			calls++
			response := &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}
			if calls == 1 {
				if req.Method != http.MethodPatch || req.URL.Query().Get("uploadType") != "resumable" ||
					req.URL.Query().Get("fields") != "id,name" || req.Header.Get("If-Match") != `"revision-1"` || req.Header.Get("X-Upload-Content-Type") != mimePDF {
					t.Fatalf("incorrect upload initialization: %s %s %v", req.Method, req.URL, req.Header)
				}
				var meta map[string]string
				if err := json.NewDecoder(req.Body).Decode(&meta); err != nil || meta["name"] != "fixtures.pdf" {
					t.Fatalf("incorrect metadata: %v (%v)", meta, err)
				}
				response.Header.Set("Location", location)
				return response, nil
			}
			if req.Method != http.MethodPut || req.URL.String() != session || req.ContentLength != int64(len(content)) {
				t.Fatalf("incorrect media request: %s %s", req.Method, req.URL)
			}
			data, err := io.ReadAll(req.Body)
			if err != nil || !bytes.Equal(data, content) {
				t.Fatalf("upload bytes did not match: %v", err)
			}
			response.Body = io.NopCloser(strings.NewReader(`{"id":"upload-target","name":"fixtures.pdf"}`))
			return response, nil
		})}
		file, err := resumableDriveUpload(t.Context(), client, http.MethodPatch, endpoint, map[string]any{"name": "fixtures.pdf"}, mimePDF, content, map[string]string{"If-Match": `"revision-1"`})
		if location == session {
			if err != nil || file.ID != "upload-target" || calls != 2 {
				t.Fatalf("the large upload didn't finish: %+v %v, %d calls", file, err, calls)
			}
		} else if err == nil || calls != 1 {
			t.Fatalf("Drive used an unexpected upload host: %v, %d calls", err, calls)
		}
	}
}
