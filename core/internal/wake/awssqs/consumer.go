package awssqs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"io"
	"log"
	"net/http"
	"nori/internal/observability"
	"sync"
	"time"
)

// Deliver resolves a configured target and creates a short-lived service token.
// It never trusts URLs, authorization or arbitrary HTTP headers in queue data.
func (w *Waker) Deliver(ctx context.Context, body string) error {
	if len(body) > 60<<10 {
		return errors.New("wake exceeds 60 KiB")
	}
	var m Message
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		return errors.New("invalid wake")
	}
	target, payload, err := w.target(m)
	if err != nil {
		return err
	}
	token, err := w.cfg.Auth.Token()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", target, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	for name, value := range observability.InjectTrace(observability.ExtractTrace(ctx, m.Trace)) {
		req.Header.Set(name, value)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 65536))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("wake target returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// ReceiveOnce removes a message only after its handler returns success. A crash,
// failed response or failed delete leaves it available for redelivery.
func (w *Waker) ReceiveOnce(ctx context.Context) error {
	out, err := w.queue.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: aws.String(w.cfg.QueueURL), MaxNumberOfMessages: 1, WaitTimeSeconds: 20, VisibilityTimeout: 780})
	if err != nil {
		return err
	}
	for _, m := range out.Messages {
		if m.ReceiptHandle == nil {
			return errors.New("SQS message has no receipt")
		}
		if err = w.Deliver(ctx, aws.ToString(m.Body)); err != nil {
			_, _ = w.queue.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{QueueUrl: aws.String(w.cfg.QueueURL), ReceiptHandle: m.ReceiptHandle, VisibilityTimeout: 30})
			return err
		}
		if _, err = w.queue.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(w.cfg.QueueURL), ReceiptHandle: m.ReceiptHandle}); err != nil {
			return err
		}
	}
	return nil
}

// Run keeps two bounded deliveries active so a worker slice cannot block conversation wakes.
func (w *Waker) Run(ctx context.Context) error {
	if err := w.cfg.Auth.Validate(); err != nil {
		return err
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				if err := w.ReceiveOnce(ctx); err != nil && ctx.Err() == nil {
					log.Printf("AWS wake will retry: %v", err)
					select {
					case <-ctx.Done():
						return
					case <-time.After(2 * time.Second):
					}
				}
			}
		}()
	}
	wg.Wait()
	return ctx.Err()
}
