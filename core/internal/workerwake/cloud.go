// Package workerwake sends durable, content-free requests to immutable workers.
package workerwake

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/api/cloudtasks/v2"
	"google.golang.org/api/option"
	"nori/internal/observability"
)

type Cloud struct {
	api           *cloudtasks.Service
	services      map[string]string
	queue, caller string
}

// NewCloud validates immutable wake routes and creates one reusable client.
// An empty services configuration disables wakes for local/native workers.
func NewCloud(ctx context.Context, rawServices, queue, caller, emulator string, local bool) (*Cloud, error) {
	if rawServices == "" {
		return nil, nil
	}
	var services map[string]string
	if err := json.Unmarshal([]byte(rawServices), &services); err != nil {
		return nil, fmt.Errorf("invalid NORI_WORKER_SERVICES: %w", err)
	}
	if len(services) == 0 {
		return nil, fmt.Errorf("NORI_WORKER_SERVICES must contain immutable worker routes")
	}
	for build, root := range services {
		root = strings.TrimRight(root, "/")
		u, err := url.Parse(root)
		loopback := err == nil && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")
		if build == "" || err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || !(u.Scheme == "https" && strings.HasSuffix(u.Hostname(), ".run.app") || local && loopback) {
			return nil, fmt.Errorf("no valid immutable worker service for build %q; retained pinned versions require their own supported host", build)
		}
		services[build] = root
	}
	parts := strings.Split(queue, "/")
	if len(parts) != 6 || parts[0] != "projects" || parts[1] == "" || parts[2] != "locations" || parts[3] == "" || parts[4] != "queues" || parts[5] == "" || strings.TrimSpace(caller) == "" {
		return nil, fmt.Errorf("NORI_WORKER_WAKE_QUEUE and NORI_WAKE_SERVICE_ACCOUNT are required")
	}
	var options []option.ClientOption
	if emulator != "" {
		if !local {
			return nil, fmt.Errorf("Cloud Tasks emulator requires TEMPORAL_LOCAL=true")
		}
		e, err := url.Parse("http://" + emulator)
		if err != nil || !(e.Hostname() == "127.0.0.1" || e.Hostname() == "localhost" || e.Hostname() == "::1") {
			return nil, fmt.Errorf("Cloud Tasks emulator must use loopback")
		}
		options = []option.ClientOption{option.WithEndpoint(e.String() + "/"), option.WithoutAuthentication()}
	}
	api, err := cloudtasks.NewService(ctx, options...)
	if err != nil {
		return nil, err
	}
	return &Cloud{api: api, services: services, queue: queue, caller: caller}, nil
}

func (c *Cloud) Enabled() bool { return c != nil }

// Schedule returns only after Cloud Tasks accepts the wake. The caller keeps its
// durable command/publication intent unacknowledged on any failure.
func (c *Cloud) Schedule(ctx context.Context, build string, workflowIDs ...string) error {
	if c == nil {
		return nil
	}
	ctx, span := otel.Tracer("nori/workerwake").Start(ctx, "worker_wake.schedule", trace.WithSpanKind(trace.SpanKindProducer))
	defer span.End()
	root, ok := c.services[build]
	if !ok {
		return fmt.Errorf("no valid immutable worker service for build %q; retained pinned versions require their own supported host", build)
	}
	body, err := json.Marshal(struct {
		WorkflowIDs []string `json:"workflow_ids,omitempty"`
	}{workflowIDs})
	if err != nil {
		return err
	}
	headers := map[string]string{"Content-Type": "application/json"}
	for name, value := range observability.InjectTrace(ctx) {
		headers[name] = value
	}
	_, err = c.api.Projects.Locations.Queues.Tasks.Create(c.queue, &cloudtasks.CreateTaskRequest{Task: &cloudtasks.Task{
		DispatchDeadline: "720s",
		HttpRequest:      &cloudtasks.HttpRequest{Url: root + "/run", HttpMethod: "POST", Headers: headers, Body: base64.StdEncoding.EncodeToString(body), OidcToken: &cloudtasks.OidcToken{ServiceAccountEmail: c.caller, Audience: root}},
	}}).Context(ctx).Do()
	if err != nil {
		observability.RecordErrorCategory(ctx, "wake.worker", err)
	}
	return err
}
