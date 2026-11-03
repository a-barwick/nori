package proactive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const routesURL = "https://routes.googleapis.com/directions/v2:computeRoutes"

// Router gives the driving time between two addresses for leaving at a time.
type Router interface {
	DriveTime(ctx context.Context, origin, destination string, departAt time.Time) (time.Duration, error)
}

// RoutesAPI is Google's Routes API with a key restricted to it. URL is routesURL; only a local test stack sets a
// loopback one.
type RoutesAPI struct {
	Key    string
	URL    string
	Client *http.Client
}

func NewRoutesAPI(key string) *RoutesAPI {
	return &RoutesAPI{Key: key, URL: routesURL, Client: &http.Client{Timeout: 15 * time.Second}}
}

var errNoRoute = errors.New("no driving route between the two places")

func (r *RoutesAPI) DriveTime(ctx context.Context, origin, destination string, departAt time.Time) (time.Duration, error) {
	body, _ := json.Marshal(map[string]any{
		"origin": map[string]string{"address": origin}, "destination": map[string]string{"address": destination},
		"travelMode": "DRIVE", "routingPreference": "TRAFFIC_AWARE", "departureTime": departAt.UTC().Format(time.RFC3339),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.URL, bytes.NewReader(body))
	if err != nil {
		return 0, errors.New("routes request could not be built")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Goog-Api-Key", r.Key)
	req.Header.Set("X-Goog-FieldMask", "routes.duration")
	res, err := r.Client.Do(req)
	if err != nil {
		return 0, errors.New("routes request failed before a response arrived")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("routes returned HTTP %d", res.StatusCode)
	}
	var out struct {
		Routes []struct {
			Duration string `json:"duration"`
		} `json:"routes"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out) != nil {
		return 0, errors.New("routes returned an unreadable response")
	}
	if len(out.Routes) == 0 {
		return 0, errNoRoute
	}
	seconds, err := strconv.ParseFloat(strings.TrimSuffix(out.Routes[0].Duration, "s"), 64)
	if err != nil || seconds < 0 {
		return 0, errors.New("routes returned an unreadable duration")
	}
	return time.Duration(seconds * float64(time.Second)), nil
}
