package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type client struct {
	base  *url.URL
	http  *http.Client
	owner string
	token string
}

type requestError struct {
	RetryAfter time.Duration `json:"-"`
	Status     int           `json:"-"`
	Code       string        `json:"code"`
	Text       string        `json:"error"`
}

func (e *requestError) Error() string {
	if e.Text != "" {
		return fmt.Sprintf("%s (HTTP %d)", e.Text, e.Status)
	}
	return fmt.Sprintf("Nori returned HTTP %d", e.Status)
}

func (c *client) do(ctx context.Context, method, path, contentType string, body []byte) (*http.Response, error) {
	prefix := ""
	if c.owner != "" {
		prefix = "users/" + url.PathEscape(c.owner) + "/"
	}
	target, err := c.base.Parse(prefix + strings.TrimPrefix(path, "/"))
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if strings.Contains(path, "/events?") {
		request.Header.Set("Accept", "text/event-stream")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return response, nil
	}
	defer response.Body.Close()
	rejection := &requestError{Status: response.StatusCode}
	if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds > 0 {
		rejection.RetryAfter = time.Duration(min(seconds, 300)) * time.Second
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(rejection)
	return nil, rejection
}

func (c *client) call(ctx context.Context, method, path string, input, output any) error {
	var body []byte
	var contentType string
	if input != nil {
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			return err
		}
		contentType = "application/json"
	}
	return c.callBytes(ctx, method, path, contentType, body, output)
}

func (c *client) callBytes(ctx context.Context, method, path, contentType string, body []byte, output any) error {
	var err error
	for attempt := range 3 {
		err = c.read(ctx, method, path, contentType, body, output)
		if err == nil || !retryable(err) || attempt == 2 {
			return err
		}
		if err := pause(ctx, retryDelay(err, time.Duration(attempt+1)*300*time.Millisecond)); err != nil {
			return err
		}
	}
	return err
}

func (c *client) read(ctx context.Context, method, path, contentType string, body []byte, output any) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	response, err := c.do(ctx, method, path, contentType, body)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if output != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(output)
	}
	return nil
}

func retryable(err error) bool {
	var rejection *requestError
	return !errors.As(err, &rejection) || rejection.Status >= 500 || rejection.Status == http.StatusTooManyRequests
}

func retryDelay(err error, fallback time.Duration) time.Duration {
	var rejection *requestError
	if errors.As(err, &rejection) && rejection.RetryAfter > 0 {
		return rejection.RetryAfter
	}
	return fallback
}

func isStatus(err error, status int) bool {
	var rejection *requestError
	return errors.As(err, &rejection) && rejection.Status == status
}

func pause(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
