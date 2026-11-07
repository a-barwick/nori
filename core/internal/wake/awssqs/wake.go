// Package awssqs delivers immediate wakes through SQS and schedules future SQS
// messages with EventBridge Scheduler. Queue bodies contain identities, not content or credentials.
package awssqs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	"github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"net/url"
	"nori/internal/dispatch"
	"nori/internal/observability"
	"nori/internal/serviceauth"
	"nori/internal/users"
	"nori/internal/wake"
	"strings"
	"time"
)

type Config struct {
	Region, QueueURL, QueueARN, ScheduleGroup, ScheduleRoleARN, DeadLetterARN string
	RuntimeURL                                                                string
	Workers                                                                   map[string]string
	Auth                                                                      serviceauth.Config
	Local                                                                     bool
}
type Waker struct {
	queue     *sqs.Client
	schedules *scheduler.Client
	cfg       Config
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
	return New(sqs.NewFromConfig(cfg), scheduler.NewFromConfig(cfg), c)
}
func New(queue *sqs.Client, schedules *scheduler.Client, c Config) (*Waker, error) {
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
	return &Waker{queue: queue, schedules: schedules, cfg: c}, nil
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
func (w *Waker) WakeWorker(ctx context.Context, build string, ids ...string) error {
	if !w.WorkersNeedWake() {
		return nil
	}
	m := w.message(ctx, "worker")
	m.Build = build
	m.WorkflowIDs = ids
	return w.send(ctx, m)
}
func (w *Waker) WakeConversationAt(ctx context.Context, at time.Time, key string) error {
	if !at.After(time.Now()) {
		return w.WakeConversation(ctx)
	}
	if key == "" || w.cfg.QueueARN == "" || w.cfg.ScheduleRoleARN == "" || w.cfg.DeadLetterARN == "" {
		return errors.New("scheduled wakes need a key, SQS ARN, Scheduler role and dead-letter ARN")
	}
	body, err := w.body(w.message(ctx, "conversation"))
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(w.cfg.QueueURL + "\x00" + users.ID(ctx) + "\x00" + key))
	name := "nori-" + hex.EncodeToString(digest[:28])
	group := w.cfg.ScheduleGroup
	if group == "" {
		group = "default"
	}
	_, err = w.schedules.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name: aws.String(name), GroupName: aws.String(group), ClientToken: aws.String(name), ScheduleExpression: aws.String("at(" + at.UTC().Format("2006-01-02T15:04:05") + ")"), ScheduleExpressionTimezone: aws.String("UTC"),
		FlexibleTimeWindow: &types.FlexibleTimeWindow{Mode: types.FlexibleTimeWindowModeOff}, ActionAfterCompletion: types.ActionAfterCompletionDelete,
		Target: &types.Target{Arn: aws.String(w.cfg.QueueARN), RoleArn: aws.String(w.cfg.ScheduleRoleARN), Input: aws.String(body), DeadLetterConfig: &types.DeadLetterConfig{Arn: aws.String(w.cfg.DeadLetterARN)}, RetryPolicy: &types.RetryPolicy{MaximumEventAgeInSeconds: aws.Int32(86400), MaximumRetryAttempts: aws.Int32(185)}},
	})
	var conflict *types.ConflictException
	if errors.As(err, &conflict) {
		return nil
	}
	return err
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
