package gcs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"nori/internal/state"
	"nori/internal/state/encrypted"
	"nori/internal/users"
)

type stubObject struct {
	name         string
	generation   string
	contentType  string
	cacheControl string
	data         []byte
	updated      time.Time
}

type gcsStub struct {
	t      *testing.T
	bucket string

	mu      sync.Mutex
	nextGen int64
	live    map[string]*stubObject
	deleted []string
}

type handlerTransport struct {
	handler http.Handler
}

func (t handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	t.handler.ServeHTTP(rec, req)
	res := rec.Result()
	res.Request = req
	return res, nil
}

func newHandlerClient(h http.Handler) *http.Client {
	return &http.Client{Transport: handlerTransport{handler: h}}
}

func newGCSStub(t *testing.T, bucket string) (*gcsStub, *Client) {
	t.Helper()
	stub := &gcsStub{
		t:       t,
		bucket:  bucket,
		nextGen: 100,
		live:    make(map[string]*stubObject),
	}
	client := NewClient(
		newHandlerClient(http.HandlerFunc(stub.serveHTTP)),
		"https://storage.test/storage/v1",
		"https://storage.test/upload/storage/v1",
	)
	return stub, client
}

func (g *gcsStub) serveHTTP(w http.ResponseWriter, r *http.Request) {
	uploadPrefix := "/upload/storage/v1/b/" + g.bucket + "/o"
	objectsPrefix := "/storage/v1/b/" + g.bucket + "/o"

	switch {
	case r.Method == http.MethodPost && r.URL.Path == uploadPrefix:
		g.handleUpload(w, r)
	case r.Method == http.MethodGet && r.URL.Path == objectsPrefix:
		g.handleList(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.EscapedPath(), objectsPrefix+"/"):
		g.handleDownload(w, r, objectsPrefix)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.EscapedPath(), objectsPrefix+"/"):
		g.handleDelete(w, r, objectsPrefix)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (g *gcsStub) handleUpload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("uploadType") != "multipart" {
		http.Error(w, "expected uploadType=multipart", http.StatusBadRequest)
		return
	}
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/related" || params["boundary"] == "" {
		http.Error(w, "expected multipart/related", http.StatusBadRequest)
		return
	}
	mr := multipart.NewReader(r.Body, params["boundary"])

	part1, err := mr.NextPart()
	if err != nil {
		http.Error(w, "missing metadata part", http.StatusBadRequest)
		return
	}
	var meta struct {
		Name         string `json:"name"`
		ContentType  string `json:"contentType"`
		CacheControl string `json:"cacheControl"`
	}
	if err := json.NewDecoder(part1).Decode(&meta); err != nil {
		http.Error(w, "bad metadata json", http.StatusBadRequest)
		return
	}

	part2, err := mr.NextPart()
	if err != nil {
		http.Error(w, "missing media part", http.StatusBadRequest)
		return
	}
	data, err := io.ReadAll(part2)
	if err != nil {
		http.Error(w, "read media failed", http.StatusInternalServerError)
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if q.Get("ifGenerationMatch") == "0" {
		if _, exists := g.live[meta.Name]; exists {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
	}
	g.nextGen++
	gen := strconv.FormatInt(g.nextGen, 10)
	g.live[meta.Name] = &stubObject{
		name:         meta.Name,
		generation:   gen,
		contentType:  meta.ContentType,
		cacheControl: meta.CacheControl,
		data:         data,
		updated:      time.Now().UTC(),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"name":       meta.Name,
		"generation": gen,
	})
}

func (g *gcsStub) handleDownload(w http.ResponseWriter, r *http.Request, objectsPrefix string) {
	if r.URL.Query().Get("alt") != "media" {
		http.Error(w, "expected alt=media", http.StatusBadRequest)
		return
	}
	escaped := strings.TrimPrefix(r.URL.EscapedPath(), objectsPrefix+"/")
	if strings.Contains(escaped, "/") {
		http.Error(w, "object name must be escaped", http.StatusBadRequest)
		return
	}
	name, err := url.PathUnescape(escaped)
	if err != nil {
		http.Error(w, "bad escape", http.StatusBadRequest)
		return
	}
	g.mu.Lock()
	obj, ok := g.live[name]
	g.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", obj.contentType)
	w.Header().Set("Cache-Control", obj.cacheControl)
	_, _ = w.Write(obj.data)
}

func (g *gcsStub) handleDelete(w http.ResponseWriter, r *http.Request, objectsPrefix string) {
	escaped := strings.TrimPrefix(r.URL.EscapedPath(), objectsPrefix+"/")
	if strings.Contains(escaped, "/") {
		http.Error(w, "object name must be escaped", http.StatusBadRequest)
		return
	}
	name, err := url.PathUnescape(escaped)
	if err != nil {
		http.Error(w, "bad escape", http.StatusBadRequest)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	obj, ok := g.live[name]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if wantGen := r.URL.Query().Get("ifGenerationMatch"); wantGen != "" && obj.generation != wantGen {
		w.WriteHeader(http.StatusPreconditionFailed)
		return
	}
	delete(g.live, name)
	g.deleted = append(g.deleted, name)
	w.WriteHeader(http.StatusNoContent)
}

func (g *gcsStub) handleList(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	g.mu.Lock()
	type item struct {
		Name       string `json:"name"`
		Generation string `json:"generation"`
		Size       string `json:"size"`
		Updated    string `json:"updated"`
	}
	var items []item
	for _, obj := range g.live {
		if strings.HasPrefix(obj.name, prefix) {
			items = append(items, item{
				Name:       obj.name,
				Generation: obj.generation,
				Size:       strconv.Itoa(len(obj.data)),
				Updated:    obj.updated.UTC().Format(time.RFC3339Nano),
			})
		}
	}
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
}

func testUser() users.User {
	return users.User{
		ID:       "11111111-1111-4111-8111-111111111111",
		Email:    "alice@example.com",
		TimeZone: "UTC",
	}
}

func TestStoreConformanceCRUDAndPreconditions(t *testing.T) {
	stub, client := newGCSStub(t, "nori-artifacts")
	user := testUser()
	store, err := client.ForUser("nori-artifacts", user)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// PutFile stores under exact artifacts/users/<id>/<key> layout with Cache-Control: private, no-store.
	key := "uploads/22222222-2222-4222-8222-222222222222"
	payload := []byte("hello world pdf bytes")
	if err := store.PutFile(ctx, key, "application/pdf", payload); err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	wantFullKey := "artifacts/users/" + user.ID + "/" + key
	stub.mu.Lock()
	stored := stub.live[wantFullKey]
	stub.mu.Unlock()
	if stored == nil {
		t.Fatalf("expected object at %q", wantFullKey)
	}
	if stored.cacheControl != "private, no-store" {
		t.Fatalf("Cache-Control = %q, want %q", stored.cacheControl, "private, no-store")
	}
	if stored.contentType != "application/pdf" {
		t.Fatalf("Content-Type = %q, want application/pdf", stored.contentType)
	}

	// Duplicate PutFile maps HTTP 412 -> state.ErrFileExists.
	if err := store.PutFile(ctx, key, "application/pdf", []byte("overwrite")); !errors.Is(err, state.ErrFileExists) {
		t.Fatalf("duplicate PutFile err = %v, want state.ErrFileExists", err)
	}

	// GetFile returns exact payload.
	got, err := store.GetFile(ctx, key)
	if err != nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("GetFile = %q, want %q", got, payload)
	}

	// GetFile on missing key maps HTTP 404 -> state.ErrNotFound.
	if _, err := store.GetFile(ctx, "uploads/33333333-3333-4333-8333-333333333333"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("missing GetFile err = %v, want state.ErrNotFound", err)
	}

	// PutPNG & GetPNG use <run>/<id>.png and image/png.
	runID := "44444444-4444-4444-8444-444444444444"
	artID := "55555555-5555-4555-8555-555555555555"
	png := []byte("\x89PNG\r\n\x1a\nfake")
	if err := store.PutPNG(ctx, runID, artID, png); err != nil {
		t.Fatalf("PutPNG: %v", err)
	}
	gotPNG, err := store.GetPNG(ctx, runID, artID)
	if err != nil {
		t.Fatalf("GetPNG: %v", err)
	}
	if !bytes.Equal(gotPNG, png) {
		t.Fatalf("GetPNG mismatch")
	}

	// DeleteFile removes the object and is idempotent on 404.
	if err := store.DeleteFile(ctx, key); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if _, err := store.GetFile(ctx, key); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("GetFile after delete = %v, want state.ErrNotFound", err)
	}
	if err := store.DeleteFile(ctx, key); err != nil {
		t.Fatalf("idempotent DeleteFile: %v", err)
	}
}

func TestPurgeUsesExactGenerationsAndPreservesOtherUsers(t *testing.T) {
	const objectsPath = "/storage/v1/b/test-bucket/o"
	const upload = "artifacts/uploads/report #1.pdf"
	const result = "artifacts/results/new"
	var mu sync.Mutex
	var deleted []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				w.WriteHeader(http.StatusNotFound)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	client := NewClient(newHandlerClient(handler), "https://storage.test/storage/v1", "https://storage.test/upload/storage/v1")
	store, err := client.ForUser("test-bucket", users.User{ID: users.LegacyID, Email: "owner@example.com", TimeZone: "UTC"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Purge(context.Background()); err != nil {
		t.Fatal(err)
	}
	slices.Sort(deleted)
	want := []string{result + "@13", upload + "@11", upload + "@12"}
	if !slices.Equal(deleted, want) {
		t.Fatalf("deleted %v, want %v", deleted, want)
	}
}

func TestPurgeRejectsForeignKeysAndReportsDeletionFailure(t *testing.T) {
	for _, tc := range []struct {
		name, listedKey, errorText string
		wantDelete                 bool
	}{
		{"foreign key", "artifacts/users/other/uploads/a", "outside the user's prefix", false},
		{"access denied", "artifacts/users/11111111-1111-4111-8111-111111111111/uploads/a", "HTTP 403", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			didDelete := make(chan struct{}, 1)
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					fmt.Fprintf(w, `{"items":[{"name":%q,"generation":"42"}]}`, tc.listedKey)
					return
				}
				didDelete <- struct{}{}
				w.WriteHeader(http.StatusForbidden)
			})
			client := NewClient(newHandlerClient(handler), "https://storage.test", "https://storage.test")
			store, err := client.ForUser("test-bucket", testUser())
			if err != nil {
				t.Fatal(err)
			}
			err = store.Purge(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.errorText) || (len(didDelete) == 1) != tc.wantDelete {
				t.Fatalf("purge error = %v, removed object = %t", err, len(didDelete) == 1)
			}
		})
	}
}

type xorSealer struct{ key byte }

func (s xorSealer) Seal(_ context.Context, scope, recordID string, plaintext []byte) ([]byte, error) {
	out := make([]byte, len(plaintext))
	for i, b := range plaintext {
		out[i] = b ^ s.key ^ byte(len(scope)+len(recordID))
	}
	return out, nil
}

func (s xorSealer) Open(ctx context.Context, scope, recordID string, ciphertext []byte) ([]byte, error) {
	return s.Seal(ctx, scope, recordID, ciphertext)
}

func TestEncryptedArtifactStoreWrappingGCS(t *testing.T) {
	stub, client := newGCSStub(t, "nori-enc")
	user := testUser()
	inner, err := client.ForUser("nori-enc", user)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := encrypted.NewArtifactStore(inner, xorSealer{key: 0x5A})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := "uploads/66666666-6666-4666-8666-666666666666"
	plaintext := []byte("sensitive user document")
	if err := enc.PutFile(ctx, key, "text/plain", plaintext); err != nil {
		t.Fatalf("encrypted PutFile: %v", err)
	}

	// Underlying GCS object must hold encrypted bytes with NORIENC1 magic, not plaintext.
	rawKey := "artifacts/users/" + user.ID + "/" + key
	stub.mu.Lock()
	raw := stub.live[rawKey].data
	stub.mu.Unlock()
	if !bytes.HasPrefix(raw, []byte(encrypted.ArtifactMagic)) {
		t.Fatalf("expected encrypted payload in GCS with %q magic", encrypted.ArtifactMagic)
	}
	if bytes.Contains(raw, plaintext) {
		t.Fatalf("raw GCS object must not contain plaintext")
	}

	// Reading through encrypted wrapper decrypts transparently.
	got, err := enc.GetFile(ctx, key)
	if err != nil {
		t.Fatalf("encrypted GetFile: %v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("encrypted GetFile = %q, want %q", got, plaintext)
	}

	// Duplicate write still surfaces state.ErrFileExists.
	if err := enc.PutFile(ctx, key, "text/plain", plaintext); !errors.Is(err, state.ErrFileExists) {
		t.Fatalf("duplicate encrypted PutFile = %v, want ErrFileExists", err)
	}
}

func TestListObjectsAndConditionalDeleteObject(t *testing.T) {
	_, client := newGCSStub(t, "nori-list")
	user := testUser()
	store, err := client.ForUser("nori-list", user)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	k1 := "uploads/77777777-7777-4777-8777-777777777777"
	if err := store.PutFile(ctx, k1, "text/plain", []byte("abc")); err != nil {
		t.Fatal(err)
	}
	objs, err := client.ListObjects(ctx, "nori-list", "artifacts/users/"+user.ID+"/")
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	if len(objs) != 1 || objs[0].Size != 3 || objs[0].Generation == "" || objs[0].Updated.IsZero() {
		t.Fatalf("unexpected listed objects: %+v", objs)
	}
	if err := client.DeleteObject(ctx, "nori-list", objs[0].Name, "999999"); err == nil {
		t.Fatalf("expected mismatched generation delete to fail")
	}
	if err := client.DeleteObject(ctx, "nori-list", objs[0].Name, objs[0].Generation); err != nil {
		t.Fatalf("DeleteObject with matching generation: %v", err)
	}
	objs, err = client.ListObjects(ctx, "nori-list", "artifacts/users/"+user.ID+"/")
	if err != nil {
		t.Fatal(err)
	}
	if len(objs) != 0 {
		t.Fatalf("expected 0 objects after delete, got %d", len(objs))
	}
}
