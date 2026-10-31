// Package workerwake sends durable, content-free requests to immutable workers.
package workerwake

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"google.golang.org/api/cloudtasks/v2"
	"google.golang.org/api/option"
)

func Enabled() bool { return os.Getenv("NORI_WORKER_SERVICES") != "" }

// Schedule returns only after Cloud Tasks accepts the wake. The caller keeps its
// existing durable command/publication intent unacknowledged on any failure.
func Schedule(ctx context.Context, build string, workflowIDs ...string) error {
	if !Enabled() {
		return nil // Existing local workers and retained native pool deployments.
	}
	var services map[string]string
	if err := json.Unmarshal([]byte(os.Getenv("NORI_WORKER_SERVICES")), &services); err != nil {
		return fmt.Errorf("invalid NORI_WORKER_SERVICES: %w", err)
	}
	root := strings.TrimRight(services[build], "/")
	u, err := url.Parse(root)
	local := os.Getenv("TEMPORAL_LOCAL") == "true"
	loopback := err == nil && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || !(u.Scheme == "https" && strings.HasSuffix(u.Hostname(), ".run.app") || local && loopback) {
		return fmt.Errorf("no valid immutable worker service for build %q; retained pinned versions require their own supported host", build)
	}
	queue, caller := os.Getenv("NORI_WORKER_WAKE_QUEUE"), os.Getenv("NORI_WAKE_SERVICE_ACCOUNT")
	parts := strings.Split(queue, "/")
	if len(parts) != 6 || parts[0] != "projects" || parts[2] != "locations" || parts[4] != "queues" || caller == "" {
		return fmt.Errorf("NORI_WORKER_WAKE_QUEUE and NORI_WAKE_SERVICE_ACCOUNT are required")
	}
	var options []option.ClientOption
	if endpoint := os.Getenv("CLOUD_TASKS_EMULATOR_HOST"); endpoint != "" {
		if !local {
			return fmt.Errorf("Cloud Tasks emulator requires TEMPORAL_LOCAL=true")
		}
		e, err := url.Parse("http://" + endpoint)
		if err != nil || !(e.Hostname() == "127.0.0.1" || e.Hostname() == "localhost" || e.Hostname() == "::1") {
			return fmt.Errorf("Cloud Tasks emulator must use loopback")
		}
		options = []option.ClientOption{option.WithEndpoint(e.String() + "/"), option.WithoutAuthentication()}
	}
	api, err := cloudtasks.NewService(ctx, options...)
	if err != nil {
		return err
	}
	body, err := json.Marshal(struct {
		WorkflowIDs []string `json:"workflow_ids,omitempty"`
	}{workflowIDs})
	if err != nil {
		return err
	}
	_, err = api.Projects.Locations.Queues.Tasks.Create(queue, &cloudtasks.CreateTaskRequest{Task: &cloudtasks.Task{
		DispatchDeadline: "720s",
		HttpRequest:      &cloudtasks.HttpRequest{Url: root + "/run", HttpMethod: "POST", Headers: map[string]string{"Content-Type": "application/json"}, Body: base64.StdEncoding.EncodeToString(body), OidcToken: &cloudtasks.OidcToken{ServiceAccountEmail: caller, Audience: root}},
	}}).Context(ctx).Do()
	return err
}
