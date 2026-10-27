// Package wake connects durable conversation work to HTTP and wake delivery.
package wake

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"google.golang.org/api/cloudtasks/v2"
)

// Cloud enqueues notifications, never conversation contents. Firestore owns
// ordering and deduplication; unnamed tasks avoid deleted-name tombstones when
// a client retries an input whose first wake could not be confirmed.
type Cloud struct {
	api                   *cloudtasks.Service
	queue, target, caller string
}

func NewCloud(ctx context.Context, queue, invokeRoot, caller string) (*Cloud, error) {
	u, err := url.Parse(strings.TrimRight(invokeRoot, "/"))
	if err != nil || u.Scheme != "https" || !strings.HasSuffix(u.Hostname(), ".googleapis.com") || u.Port() != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(u.Path, "/api") {
		return nil, fmt.Errorf("NORI_RUNTIME_URL must be the native HTTPS Agent Runtime invoke root ending in /api")
	}
	parts := strings.Split(queue, "/")
	if len(parts) != 6 || parts[0] != "projects" || parts[1] == "" || parts[2] != "locations" || parts[3] == "" || parts[4] != "queues" || parts[5] == "" || strings.TrimSpace(caller) == "" {
		return nil, fmt.Errorf("NORI_WAKE_QUEUE and NORI_WAKE_SERVICE_ACCOUNT must name the Cloud Tasks queue and its caller")
	}
	api, err := cloudtasks.NewService(ctx)
	if err != nil {
		return nil, err
	}
	return &Cloud{api: api, queue: queue, target: u.String() + "/api/process", caller: caller}, nil
}

func (c *Cloud) Schedule(ctx context.Context) error {
	_, err := c.api.Projects.Locations.Queues.Tasks.Create(c.queue, &cloudtasks.CreateTaskRequest{Task: &cloudtasks.Task{
		DispatchDeadline: "600s",
		HttpRequest:      &cloudtasks.HttpRequest{Url: c.target, HttpMethod: "POST", Headers: map[string]string{"Content-Type": "application/json"}, Body: base64.StdEncoding.EncodeToString([]byte("{}")), OauthToken: &cloudtasks.OAuthToken{ServiceAccountEmail: c.caller, Scope: "https://www.googleapis.com/auth/cloud-platform"}},
	}}).Context(ctx).Do()
	return err
}
