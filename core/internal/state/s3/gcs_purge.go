package s3

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"golang.org/x/oauth2/google"
	"golang.org/x/sync/errgroup"
)

const (
	gcsObjectAPI  = "https://storage.googleapis.com/storage/v1"
	gcsWriteScope = "https://www.googleapis.com/auth/devstorage.read_write"
)

type gcsObjectVersion struct {
	Name       string `json:"name"`
	Generation string `json:"generation"`
}

func (s *Store) purgeGoogleStorage(ctx context.Context) error {
	client, err := google.DefaultClient(ctx, gcsWriteScope)
	if err != nil {
		return fmt.Errorf("authenticate Cloud Storage reset: %w", err)
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return s.purgeGCS(ctx, client, gcsObjectAPI)
}

func (s *Store) purgeGCS(ctx context.Context, client *http.Client, apiURL string) error {
	objectsURL := apiURL + "/b/" + url.PathEscape(s.bucket) + "/o"
	query := url.Values{
		"prefix":     {s.prefix},
		"versions":   {"true"},
		"maxResults": {"1000"},
		"fields":     {"nextPageToken,items(name,generation)"},
	}
	var versions []gcsObjectVersion
	for {
		page, token, err := listGCSVersions(ctx, client, objectsURL+"?"+query.Encode())
		if err != nil {
			return err
		}
		for _, version := range page {
			allowed, err := s.purgeObjectAllowed(version.Name)
			if err != nil {
				return err
			}
			if !allowed {
				continue
			}
			if generation, err := strconv.ParseUint(version.Generation, 10, 64); err != nil || generation == 0 {
				return errors.New("Cloud Storage listed an invalid object generation")
			}
			versions = append(versions, version)
		}
		if token == "" {
			break
		}
		if token == query.Get("pageToken") {
			return errors.New("Cloud Storage repeated an object listing page token")
		}
		query.Set("pageToken", token)
	}

	group, deleteCtx := errgroup.WithContext(ctx)
	group.SetLimit(8)
	for _, version := range versions {
		if deleteCtx.Err() != nil {
			break
		}
		group.Go(func() error {
			return deleteGCSVersion(deleteCtx, client, objectsURL, version)
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	return ctx.Err()
}

func listGCSVersions(ctx context.Context, client *http.Client, endpoint string) ([]gcsObjectVersion, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("list Cloud Storage object versions: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("list Cloud Storage object versions: HTTP %d", res.StatusCode)
	}
	var page struct {
		Items         []gcsObjectVersion `json:"items"`
		NextPageToken string             `json:"nextPageToken"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(&page); err != nil {
		return nil, "", fmt.Errorf("decode Cloud Storage object versions: %w", err)
	}
	return page.Items, page.NextPageToken, nil
}

func deleteGCSVersion(ctx context.Context, client *http.Client, objectsURL string, version gcsObjectVersion) error {
	endpoint := objectsURL + "/" + url.PathEscape(version.Name) + "?" + url.Values{"generation": {version.Generation}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("delete Cloud Storage object generation: %w", err)
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusNoContent, http.StatusNotFound:
		return nil
	default:
		return fmt.Errorf("delete Cloud Storage object generation: HTTP %d", res.StatusCode)
	}
}
