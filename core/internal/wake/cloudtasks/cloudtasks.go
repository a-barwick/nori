// Package cloudtasks implements wake.Waker with Google Cloud Tasks. It is the
// only place that knows queue names, task bodies and how Cloud Tasks
// authenticates to its targets.
//
// Cloud Tasks enqueues notifications, never conversation contents. Firestore
// owns ordering and deduplication; unnamed tasks avoid deleted-name tombstones
// when a client retries an input whose first wake could not be confirmed. A
// timed wake is named by its key, because it is asked for once per planned time.
package cloudtasks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"google.golang.org/api/googleapi"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nori/internal/observability"
	"nori/internal/users"
	"nori/internal/wake"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/api/cloudtasks/v2"
	"google.golang.org/api/option"
)

var tracer = otel.Tracer("nori/wake")

// Config selects which wakes this process can send. A nil section leaves that
// wake unconfigured: WakeConversation then returns wake.ErrNotConfigured, and
// WakeWorker does nothing because workers poll on their own.
type Config struct {
	Conversation *Conversation
	Workers      *Workers
	// Local permits loopback http targets, for emulated and native local runs.
	Local bool
	// Emulator is the host:port of a loopback Cloud Tasks emulator. Local only.
	Emulator string
}

// Conversation is the target of conversation wakes: the API's process route.
type Conversation struct {
	// Queue is projects/PROJECT/locations/LOCATION/queues/QUEUE.
	Queue string
	// InvokeRoot is the API's base URL. Wakes POST to InvokeRoot + "/api/process".
	InvokeRoot string
	// Caller is the service account whose OAuth token authenticates the wake.
	Caller string
}

// Workers are the targets of worker wakes.
type Workers struct {
	Queue string
	// Caller is the service account whose OIDC token authenticates the wake.
	Caller string
	// Services maps each immutable worker build to the root URL of its service.
	Services map[string]string
}

type Waker struct {
	api          *cloudtasks.Service
	conversation *conversationTarget
	workers      *workerTargets
}

type conversationTarget struct{ queue, url, caller string }
type workerTargets struct {
	queue, caller string
	services      map[string]string
}

var _ wake.Waker = (*Waker)(nil)

// New validates every configured target and creates one reusable client.
func New(ctx context.Context, cfg Config) (*Waker, error) {
	w := &Waker{}
	if c := cfg.Conversation; c != nil {
		target, err := conversationURL(c.InvokeRoot, cfg.Local)
		if err != nil {
			return nil, err
		}
		if err = checkQueue(c.Queue, c.Caller); err != nil {
			return nil, fmt.Errorf("conversation wake: %w", err)
		}
		w.conversation = &conversationTarget{queue: c.Queue, url: target, caller: c.Caller}
	}
	if s := cfg.Workers; s != nil {
		services, err := workerRoutes(s.Services, cfg.Local)
		if err != nil {
			return nil, err
		}
		if err = checkQueue(s.Queue, s.Caller); err != nil {
			return nil, fmt.Errorf("worker wake: %w", err)
		}
		w.workers = &workerTargets{queue: s.Queue, caller: s.Caller, services: services}
	}
	var options []option.ClientOption
	if cfg.Emulator != "" {
		if !cfg.Local {
			return nil, fmt.Errorf("Cloud Tasks emulator requires local mode")
		}
		e, err := url.Parse("http://" + cfg.Emulator)
		if err != nil || !loopback(e.Hostname()) {
			return nil, fmt.Errorf("Cloud Tasks emulator must use loopback")
		}
		options = []option.ClientOption{option.WithEndpoint(e.String() + "/"), option.WithoutAuthentication()}
	}
	api, err := cloudtasks.NewService(ctx, options...)
	if err != nil {
		return nil, err
	}
	w.api = api
	return w, nil
}

func (w *Waker) WorkersNeedWake() bool { return w.workers != nil }

func (w *Waker) WakeConversation(ctx context.Context) error {
	if !users.ValidID(users.ID(ctx)) {
		return fmt.Errorf("conversation wake requires a user")
	}
	if w.conversation == nil {
		return wake.ErrNotConfigured
	}
	observability.RequireRequestFlush(ctx)
	ctx, span := tracer.Start(ctx, "cloud_tasks.schedule", trace.WithSpanKind(trace.SpanKindProducer))
	defer span.End()
	c := w.conversation
	err := w.create(ctx, c.queue, &cloudtasks.Task{
		DispatchDeadline: "600s",
		HttpRequest: &cloudtasks.HttpRequest{
			Url: userURL(c.url, users.ID(ctx)), HttpMethod: "POST", Headers: requestHeaders(ctx), Body: base64.StdEncoding.EncodeToString([]byte("{}")),
			OauthToken: &cloudtasks.OAuthToken{ServiceAccountEmail: c.caller, Scope: "https://www.googleapis.com/auth/cloud-platform"},
		},
	})
	if err != nil {
		observability.RecordErrorCategory(ctx, "wake.conversation", err)
	}
	return err
}

// WakeConversationAt schedules a conversation wake at a future time. The task name derives from the user and the
// minute, so repeated arming for the same due time collapses into one task.
func (w *Waker) WakeConversationAt(ctx context.Context, at time.Time) error {
	id := users.ID(ctx)
	if !users.ValidID(id) {
		return fmt.Errorf("conversation wake requires a user")
	}
	if w.conversation == nil {
		return wake.ErrNotConfigured
	}
	c := w.conversation
	at = at.UTC().Truncate(time.Minute).Add(time.Minute)
	name := fmt.Sprintf("%s/tasks/due-%s-%d", c.queue, strings.ReplaceAll(id, "-", ""), at.Unix())
	err := w.create(ctx, c.queue, &cloudtasks.Task{
		Name: name, ScheduleTime: at.Format(time.RFC3339), DispatchDeadline: "600s",
		HttpRequest: &cloudtasks.HttpRequest{
			Url: userURL(c.url, id), HttpMethod: "POST", Headers: requestHeaders(ctx), Body: base64.StdEncoding.EncodeToString([]byte("{}")),
			OauthToken: &cloudtasks.OAuthToken{ServiceAccountEmail: c.caller, Scope: "https://www.googleapis.com/auth/cloud-platform"},
		},
	})
	var apiErr *googleapi.Error
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusConflict {
		return nil
	}
	return err
}

func (w *Waker) WakeWorker(ctx context.Context, build string, workflowIDs ...string) error {
	if w.workers == nil {
		return nil
	}
	ctx, span := tracer.Start(ctx, "worker_wake.schedule", trace.WithSpanKind(trace.SpanKindProducer))
	defer span.End()
	root, ok := w.workers.services[build]
	if !ok {
		return fmt.Errorf("no valid immutable worker service for build %q; retained pinned versions require their own supported host", build)
	}
	body, err := json.Marshal(struct {
		WorkflowIDs []string `json:"workflow_ids,omitempty"`
	}{workflowIDs})
	if err != nil {
		return err
	}
	err = w.create(ctx, w.workers.queue, &cloudtasks.Task{
		DispatchDeadline: "720s",
		HttpRequest: &cloudtasks.HttpRequest{
			Url: root + "/run", HttpMethod: "POST", Headers: requestHeaders(ctx), Body: base64.StdEncoding.EncodeToString(body),
			OidcToken: &cloudtasks.OidcToken{ServiceAccountEmail: w.workers.caller, Audience: root},
		},
	})
	if err != nil {
		observability.RecordErrorCategory(ctx, "wake.worker", err)
	}
	return err
}

func (w *Waker) create(ctx context.Context, queue string, task *cloudtasks.Task) error {
	_, err := w.api.Projects.Locations.Queues.Tasks.Create(queue, &cloudtasks.CreateTaskRequest{Task: task}).Context(ctx).Do()
	return err
}

func requestHeaders(ctx context.Context) map[string]string {
	headers := map[string]string{"Content-Type": "application/json"}

	for name, value := range observability.InjectTrace(ctx) {
		headers[name] = value
	}
	return headers
}

// conversationURL accepts an https URL, or loopback http when local, and
// returns the process route below it.
func conversationURL(root string, local bool) (string, error) {
	u, err := url.Parse(strings.TrimRight(root, "/"))
	if err != nil || !safeTarget(u, local) {
		return "", fmt.Errorf("conversation wake target must be an https URL with no credentials, query or fragment")
	}
	return u.String() + "/api/process", nil
}

func workerRoutes(raw map[string]string, local bool) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("worker wake needs at least one immutable worker route")
	}
	services := make(map[string]string, len(raw))
	for build, root := range raw {
		root = strings.TrimRight(root, "/")
		u, err := url.Parse(root)
		if build == "" || err != nil || u.Path != "" || !safeTarget(u, local) {
			return nil, fmt.Errorf("no valid immutable worker service for build %q; retained pinned versions require their own supported host", build)
		}
		services[build] = root
	}
	return services, nil
}

// safeTarget is the host-independent safety check for wake targets: https, or
// loopback http for local runs, with no credentials, query or fragment.
func safeTarget(u *url.URL, local bool) bool {
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return u.Scheme == "https" || (local && u.Scheme == "http" && loopback(u.Hostname()))
}

func loopback(host string) bool { return host == "127.0.0.1" || host == "localhost" || host == "::1" }

func checkQueue(queue, caller string) error {
	parts := strings.Split(queue, "/")
	if len(parts) != 6 || parts[0] != "projects" || parts[1] == "" || parts[2] != "locations" || parts[3] == "" ||
		parts[4] != "queues" || parts[5] == "" || strings.TrimSpace(caller) == "" {
		return fmt.Errorf("a Cloud Tasks queue (projects/PROJECT/locations/LOCATION/queues/QUEUE) and its caller are required")
	}
	return nil
}

func userURL(target, id string) string {
	return strings.TrimSuffix(target, "/api/process") + "/users/" + url.PathEscape(id) + "/api/process"
}
