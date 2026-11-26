package s3

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestPurgeGCSUsesExactGenerationsAndPreservesOtherUsers(t *testing.T) {
	const objectsPath = "/storage/v1/b/test-bucket/o"
	const upload = "artifacts/uploads/report #1.pdf"
	const result = "artifacts/results/new"
	var mu sync.Mutex
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path != objectsPath || query.Get("prefix") != "artifacts/" || query.Get("versions") != "true" {
				t.Errorf("unexpected version list: %s", r.URL)
			}
			switch query.Get("pageToken") {
			case "":
				fmt.Fprintf(w, `{"nextPageToken":"next/+","items":[{"name":%q,"generation":"11"},{"name":"artifacts/users/other/uploads/keep","generation":"99"}]}`, upload)
			case "next/+":
				fmt.Fprintf(w, `{"items":[{"name":%q,"generation":"12"},{"name":%q,"generation":"13"}]}`, upload, result)
			default:
				t.Errorf("unexpected page token: %q", query.Get("pageToken"))
				http.Error(w, "bad page", http.StatusBadRequest)
			}
		case http.MethodDelete:
			escapedKey := strings.TrimPrefix(r.URL.EscapedPath(), objectsPath+"/")
			key, err := url.PathUnescape(escapedKey)
			if err != nil || strings.Contains(escapedKey, "/") {
				t.Errorf("object must be an escaped path segment: %q, %v", escapedKey, err)
			}
			mu.Lock()
			deleted = append(deleted, key+"@"+query.Get("generation"))
			mu.Unlock()
			if query.Get("generation") == "11" {
				w.WriteHeader(http.StatusNotFound) // Already removed on a previous attempt.
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	store := &Store{bucket: "test-bucket", prefix: "artifacts/", legacy: true}
	if err := store.purgeGCS(context.Background(), server.Client(), server.URL+"/storage/v1"); err != nil {
		t.Fatal(err)
	}
	slices.Sort(deleted)
	want := []string{result + "@13", upload + "@11", upload + "@12"}
	if !slices.Equal(deleted, want) {
		t.Fatalf("deleted %v, want %v", deleted, want)
	}
}

func TestPurgeGCSRejectsForeignKeysAndReportsDeletionFailure(t *testing.T) {
	for _, tc := range []struct {
		name, listedKey, errorText string
		wantDelete                 bool
	}{
		{"foreign key", "artifacts/users/other/uploads/a", "outside the user's prefix", false},
		{"access denied", "artifacts/users/current/uploads/a", "HTTP 403", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			didDelete := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					fmt.Fprintf(w, `{"items":[{"name":%q,"generation":"42"}]}`, tc.listedKey)
					return
				}
				didDelete <- struct{}{}
				w.WriteHeader(http.StatusForbidden)
			}))
			defer server.Close()
			store := &Store{bucket: "test-bucket", prefix: "artifacts/users/current/"}
			err := store.purgeGCS(context.Background(), server.Client(), server.URL)
			if err == nil || !strings.Contains(err.Error(), tc.errorText) || (len(didDelete) == 1) != tc.wantDelete {
				t.Fatalf("purge error = %v, removed object = %t", err, len(didDelete) == 1)
			}
		})
	}
}
