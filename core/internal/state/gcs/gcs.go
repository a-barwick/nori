package gcs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2/google"
	"nori/internal/state"
	"nori/internal/users"
)

const (
	DefaultAPIURL    = "https://storage.googleapis.com/storage/v1"
	DefaultUploadURL = "https://storage.googleapis.com/upload/storage/v1"
	gcsWriteScope    = "https://www.googleapis.com/auth/devstorage.read_write"
)

// Client talks directly to the Google Cloud Storage JSON API using ADC / Workload Identity.
type Client struct {
	http      *http.Client
	apiURL    string
	uploadURL string
}

// Store implements state.ArtifactStore for a single user's prefix in a GCS bucket.
type Store struct {
	client         *Client
	bucket, prefix string
	legacy         bool
}

var _ state.ArtifactStore = (*Store)(nil)

// ObjectMeta holds metadata returned when listing GCS objects.
type ObjectMeta struct {
	Name       string
	Generation string
	Size       int64
	Updated    time.Time
}

// OpenClient initializes a GCS JSON API client using Application Default Credentials.
func OpenClient(ctx context.Context) (*Client, error) {
	client, err := google.DefaultClient(ctx, gcsWriteScope)
	if err != nil {
		return nil, fmt.Errorf("authenticate Cloud Storage client: %w", err)
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return NewClient(client, DefaultAPIURL, DefaultUploadURL), nil
}

// NewClient creates a GCS client using the supplied HTTP client and base URLs.
func NewClient(httpClient *http.Client, apiURL, uploadURL string) *Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}
	if uploadURL == "" {
		uploadURL = DefaultUploadURL
	}
	return &Client{
		http:      httpClient,
		apiURL:    strings.TrimRight(apiURL, "/"),
		uploadURL: strings.TrimRight(uploadURL, "/"),
	}
}

// ForUser scopes the client to a specific bucket and user prefix.
func (c *Client) ForUser(bucket string, user users.User) (*Store, error) {
	return New(c, bucket, user)
}

// Open initializes a GCS client with ADC and returns a Store scoped to user.
func Open(ctx context.Context, bucket string, user users.User) (*Store, error) {
	if err := user.Validate(); err != nil {
		return nil, err
	}
	c, err := OpenClient(ctx)
	if err != nil {
		return nil, err
	}
	return New(c, bucket, user)
}

// New constructs a user-scoped Store backed by an existing Client.
func New(client *Client, bucket string, user users.User) (*Store, error) {
	if client == nil {
		return nil, errors.New("gcs client is required")
	}
	if err := user.Validate(); err != nil {
		return nil, err
	}
	return &Store{
		client: client,
		bucket: bucket,
		prefix: "artifacts/" + user.FilePrefix(),
		legacy: user.ID == users.LegacyID,
	}, nil
}

func (s *Store) put(ctx context.Context, key, media string, data []byte) error {
	if s.bucket == "" {
		return errors.New("ARTIFACT_BUCKET must be configured")
	}
	if media == "" {
		media = "application/octet-stream"
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)

	metaHeader := make(textproto.MIMEHeader)
	metaHeader.Set("Content-Type", "application/json; charset=UTF-8")
	metaPart, err := mw.CreatePart(metaHeader)
	if err != nil {
		return err
	}
	metaPayload, err := json.Marshal(struct {
		Name         string `json:"name"`
		ContentType  string `json:"contentType"`
		CacheControl string `json:"cacheControl"`
	}{
		Name:         s.prefix + key,
		ContentType:  media,
		CacheControl: "private, no-store",
	})
	if err != nil {
		return err
	}
	if _, err := metaPart.Write(metaPayload); err != nil {
		return err
	}

	mediaHeader := make(textproto.MIMEHeader)
	mediaHeader.Set("Content-Type", media)
	mediaPart, err := mw.CreatePart(mediaHeader)
	if err != nil {
		return err
	}
	if _, err := mediaPart.Write(data); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}

	query := url.Values{
		"uploadType":        {"multipart"},
		"ifGenerationMatch": {"0"},
	}
	endpoint := s.client.uploadURL + "/b/" + url.PathEscape(s.bucket) + "/o?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "multipart/related; boundary="+mw.Boundary())
	res, err := s.client.http.Do(req)
	if err != nil {
		return fmt.Errorf("upload Cloud Storage object: %w", err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))

	switch res.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusPreconditionFailed:
		return state.ErrFileExists
	default:
		return fmt.Errorf("upload Cloud Storage object: HTTP %d", res.StatusCode)
	}
}

func (s *Store) PutPNG(ctx context.Context, run, id string, data []byte) error {
	return s.put(ctx, run+"/"+id+".png", "image/png", data)
}

func (s *Store) PutFile(ctx context.Context, key, media string, data []byte) error {
	return s.put(ctx, key, media, data)
}

func (s *Store) get(ctx context.Context, key string, artifact bool) ([]byte, error) {
	if s.bucket == "" {
		return nil, errors.New("ARTIFACT_BUCKET must be configured")
	}
	endpoint := s.client.apiURL + "/b/" + url.PathEscape(s.bucket) + "/o/" + url.PathEscape(s.prefix+key) + "?alt=media"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	res, err := s.client.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download Cloud Storage object: %w", err)
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusOK:
		if artifact {
			return state.ReadArtifact(res.Body)
		}
		return state.ReadFile(res.Body)
	case http.StatusNotFound:
		return nil, state.ErrNotFound
	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		return nil, fmt.Errorf("download Cloud Storage object: HTTP %d", res.StatusCode)
	}
}

func (s *Store) GetPNG(ctx context.Context, run, id string) ([]byte, error) {
	return s.get(ctx, run+"/"+id+".png", true)
}

func (s *Store) GetFile(ctx context.Context, key string) ([]byte, error) {
	return s.get(ctx, key, false)
}

func (s *Store) DeleteFile(ctx context.Context, key string) error {
	if s.bucket == "" {
		return errors.New("ARTIFACT_BUCKET must be configured")
	}
	endpoint := s.client.apiURL + "/b/" + url.PathEscape(s.bucket) + "/o/" + url.PathEscape(s.prefix+key)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	res, err := s.client.http.Do(req)
	if err != nil {
		return fmt.Errorf("delete Cloud Storage object: %w", err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	switch res.StatusCode {
	case http.StatusNoContent, http.StatusOK, http.StatusNotFound:
		return nil
	default:
		return fmt.Errorf("delete Cloud Storage object: HTTP %d", res.StatusCode)
	}
}

func (s *Store) Close() error { return nil }

// ListObjects lists live objects under prefix in bucket using pagination.
func (c *Client) ListObjects(ctx context.Context, bucket, prefix string) ([]ObjectMeta, error) {
	if bucket == "" {
		return nil, errors.New("ARTIFACT_BUCKET must be configured")
	}
	objectsURL := c.apiURL + "/b/" + url.PathEscape(bucket) + "/o"
	query := url.Values{
		"prefix":     {prefix},
		"maxResults": {"1000"},
		"fields":     {"nextPageToken,items(name,generation,size,updated)"},
	}
	var out []ObjectMeta
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, objectsURL+"?"+query.Encode(), nil)
		if err != nil {
			return nil, err
		}
		res, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list Cloud Storage objects: %w", err)
		}
		var page struct {
			Items []struct {
				Name       string `json:"name"`
				Generation string `json:"generation"`
				Size       string `json:"size"`
				Updated    string `json:"updated"`
			} `json:"items"`
			NextPageToken string `json:"nextPageToken"`
		}
		status := res.StatusCode
		var decodeErr error
		if status == http.StatusOK {
			decodeErr = json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&page)
		} else {
			_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		}
		res.Body.Close()
		if status != http.StatusOK {
			return nil, fmt.Errorf("list Cloud Storage objects: HTTP %d", status)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("decode Cloud Storage objects: %w", decodeErr)
		}
		for _, item := range page.Items {
			var size int64
			if item.Size != "" {
				parsed, err := strconv.ParseInt(item.Size, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("invalid object size %q for %q: %w", item.Size, item.Name, err)
				}
				size = parsed
			}
			var updated time.Time
			if item.Updated != "" {
				parsed, err := time.Parse(time.RFC3339Nano, item.Updated)
				if err != nil {
					return nil, fmt.Errorf("invalid updated timestamp %q for %q: %w", item.Updated, item.Name, err)
				}
				updated = parsed.UTC()
			}
			out = append(out, ObjectMeta{
				Name:       item.Name,
				Generation: item.Generation,
				Size:       size,
				Updated:    updated,
			})
		}
		if page.NextPageToken == "" {
			break
		}
		if page.NextPageToken == query.Get("pageToken") {
			return nil, errors.New("Cloud Storage repeated an object listing page token")
		}
		query.Set("pageToken", page.NextPageToken)
	}
	return out, nil
}

// DeleteObject deletes a live object, optionally guarded by ifGenerationMatch when generation is non-empty.
func (c *Client) DeleteObject(ctx context.Context, bucket, name, generation string) error {
	if bucket == "" {
		return errors.New("ARTIFACT_BUCKET must be configured")
	}
	endpoint := c.apiURL + "/b/" + url.PathEscape(bucket) + "/o/" + url.PathEscape(name)
	if generation != "" {
		endpoint += "?" + url.Values{"ifGenerationMatch": {generation}}.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("delete Cloud Storage object: %w", err)
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	switch res.StatusCode {
	case http.StatusNoContent, http.StatusOK, http.StatusNotFound:
		return nil
	default:
		return fmt.Errorf("delete Cloud Storage object: HTTP %d", res.StatusCode)
	}
}
