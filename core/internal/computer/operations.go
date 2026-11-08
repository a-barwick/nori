package computer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"nori/internal/computer/rpc"
)

type RPCError struct {
	Path   string
	Status int
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("computer %s returned HTTP %d", e.Path, e.Status)
}
func Transient(err error) bool {
	var e *RPCError
	return errors.As(err, &e) && (e.Status == 429 || e.Status == 502 || e.Status == 503 || e.Status == 504)
}

type OperationReceipt struct {
	ID          string `json:"id"`
	RunID       string `json:"run_id"`
	Status      string `json:"status"`
	HTTPStatus  int    `json:"http_status"`
	ContentType string `json:"content_type"`
	Body        []byte `json:"body"`
}

func (c *Client) Operation(ctx context.Context, id string) (OperationReceipt, error) {
	var out OperationReceipt
	data, err := c.post(ctx, rpc.OperationPath, map[string]string{"id": id, "run_id": c.run}, 3<<20)
	if err == nil {
		err = json.Unmarshal(data, &out)
	}
	if err == nil && (out.ID != id || out.RunID != c.run) {
		err = errors.New("operation receipt identity mismatch")
	}
	return out, err
}
func (c *Client) CancelRead(ctx context.Context, id string) error {
	data, err := c.post(ctx, rpc.CancelReadPath, map[string]string{"id": id, "run_id": c.run}, 4096)
	if err != nil {
		return err
	}
	var out struct {
		Stopped bool `json:"stopped"`
	}
	if err = json.Unmarshal(data, &out); err != nil {
		return err
	}
	if !out.Stopped {
		return errors.New("read interruption unconfirmed")
	}
	return nil
}
