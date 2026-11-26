package google

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func resumableDriveUpload(ctx context.Context, client *http.Client, method, endpoint string, meta map[string]any, mediaType string, data []byte, headers map[string]string) (driveFile, error) {
	var file driveFile
	start, err := url.Parse(endpoint)
	if err != nil {
		return file, err
	}
	query := start.Query()
	query.Set("uploadType", "resumable")
	start.RawQuery = query.Encode()
	metadata, err := json.Marshal(meta)
	if err != nil {
		return file, err
	}
	initHeaders := map[string]string{
		"Content-Type":            "application/json; charset=UTF-8",
		"X-Upload-Content-Type":   mediaType,
		"X-Upload-Content-Length": strconv.Itoa(len(data)),
	}
	maps.Copy(initHeaders, headers)
	sessionClient := *client
	sessionClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	_, responseHeaders, err := request(ctx, &sessionClient, method, start.String(), bytes.NewReader(metadata), initHeaders, 1<<20)
	if err != nil {
		return file, err
	}
	sessionURL, err := url.Parse(responseHeaders.Get("Location"))
	if err != nil || sessionURL == nil || sessionURL.Scheme != "https" || sessionURL.User != nil || sessionURL.Port() != "" ||
		(sessionURL.Hostname() != "www.googleapis.com" && sessionURL.Hostname() != "drive.googleapis.com") ||
		!strings.HasPrefix(sessionURL.Path, "/upload/drive/v3/files") || sessionURL.Query().Get("upload_id") == "" {
		return file, errors.New("Google Drive did not return a valid upload session")
	}
	result, _, err := request(ctx, &sessionClient, http.MethodPut, sessionURL.String(), bytes.NewReader(data),
		map[string]string{"Content-Type": mediaType}, 2<<20)
	if err != nil {
		var requestError *url.Error
		if errors.As(err, &requestError) {
			err = requestError.Err
		}
		return file, err
	}
	err = json.Unmarshal(result, &file)
	return file, err
}
