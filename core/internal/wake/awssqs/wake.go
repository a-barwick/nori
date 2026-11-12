// Package awssqs delivers wakes through SQS. Queue bodies contain identities, not content or credentials.
package awssqs

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"nori/internal/dispatch"
	"nori/internal/observability"
	"nori/internal/serviceauth"
	"nori/internal/users"
	"nori/internal/wake"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type Config struct {
	Region, QueueURL string
	RuntimeURL       string
	Workers          map[string]string
	Auth             serviceauth.Config
	InternalKey      string
	Local            bool
}
type Waker struct {
	queue *sqs.Client
	cfg   Config
}
type Message struct {
	Kind        string            `json:"kind"`
	UserID      string            `json:"user_id"`
	Build       string            `json:"build,omitempty"`
	WorkflowIDs []string          `json:"workflow_ids,omitempty"`
	Trace       map[string]string `json:"trace,omitempty"`
}

var _ wake.Waker = (*Waker)(nil)

func Open(ctx context.Context, c Config) (*Waker, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(c.Region))
	if err != nil {
		return nil, err
	}
	return New(sqs.NewFromConfig(cfg), c)
}
func New(queue *sqs.Client, c Config) (*Waker, error) {
	if c.Region == "" || c.QueueURL == "" {
		return nil, errors.New("AWS wake region and queue URL are required")
	}
	u, err := url.Parse(c.QueueURL)
	if err != nil || !safe(u, c.Local) || strings.HasSuffix(u.Path, ".fifo") {
		return nil, errors.New("AWS wake queue must be a standard SQS queue URL")
	}
	if c.RuntimeURL != "" {
		u, err = url.Parse(c.RuntimeURL)
		if err != nil || !safe(u, c.Local) {
			return nil, errors.New("invalid conversation wake URL")
		}
		c.RuntimeURL = strings.TrimRight(c.RuntimeURL, "/")
	}
	routes := map[string]string{}
	for build, root := range c.Workers {
		u, err = url.Parse(root)
		if build == "" || err != nil || !safe(u, c.Local) || (u.Path != "" && u.Path != "/") {
			return nil, errors.New("invalid immutable worker wake route")
		}
		routes[build] = strings.TrimRight(root, "/")
	}
	c.Workers = routes
	return &Waker{queue: queue, cfg: c}, nil
}
func safe(u *url.URL, local bool) bool {
	return u != nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Scheme == "https" || (local && u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1")))
}
func (w *Waker) WorkersNeedWake() bool { return len(w.cfg.Workers) > 0 }
func (w *Waker) message(ctx context.Context, kind string) Message {
	return Message{Kind: kind, UserID: users.ID(ctx), Trace: observability.InjectTrace(ctx)}
}
func (w *Waker) body(m Message) (string, error) {
	if !users.ValidID(m.UserID) {
		return "", errors.New("wake requires a user")
	}
	switch m.Kind {
	case "conversation":
		if w.cfg.RuntimeURL == "" {
			return "", wake.ErrNotConfigured
		}
	case "worker":
		if w.cfg.Workers[m.Build] == "" {
			return "", errors.New("worker build has no configured wake route")
		}
		for _, id := range m.WorkflowIDs {
			user, _, ok := dispatch.WorkflowUser(id)
			if !ok || user != m.UserID || len(id) > 256 {
				return "", errors.New("worker wake has an invalid task identity")
			}
		}
	default:
		return "", errors.New("invalid wake kind")
	}
	body, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	if len(body) > 60<<10 {
		return "", errors.New("wake exceeds 60 KiB")
	}
	return string(body), nil
}
func (w *Waker) send(ctx context.Context, m Message) error {
	body, err := w.body(m)
	if err != nil {
		return err
	}
	_, err = w.queue.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(w.cfg.QueueURL), MessageBody: aws.String(body)})
	return err
}
func (w *Waker) WakeConversation(ctx context.Context) error {
	return w.send(ctx, w.message(ctx, "conversation"))
}

// WakeConversationAt delays a conversation wake. SQS delays at most 15 minutes, so a farther due time arrives as a
// hop: the woken pass finds nothing due yet and arms the next wake.
func (w *Waker) WakeConversationAt(ctx context.Context, at time.Time) error {
	body, err := w.body(w.message(ctx, "conversation"))
	if err != nil {
		return err
	}
	delay := int32(min(max(time.Until(at).Seconds()+1, 0), 900))
	_, err = w.queue.SendMessage(ctx, &sqs.SendMessageInput{QueueUrl: aws.String(w.cfg.QueueURL), MessageBody: aws.String(body), DelaySeconds: delay})
	return err
}

func (w *Waker) WakeWorker(ctx context.Context, build string, ids ...string) error {
	if !w.WorkersNeedWake() {
		return nil
	}
	m := w.message(ctx, "worker")
	m.Build = build
	m.WorkflowIDs = ids
	return w.send(ctx, m)
}
func (w *Waker) target(m Message) (string, []byte, error) {
	if _, err := w.body(m); err != nil {
		return "", nil, err
	}
	if m.Kind == "conversation" {
		return w.cfg.RuntimeURL + "/users/" + url.PathEscape(m.UserID) + "/api/process", []byte("{}"), nil
	}
	body, err := json.Marshal(map[string]any{"workflow_ids": m.WorkflowIDs})
	return w.cfg.Workers[m.Build] + "/run", body, err
}
