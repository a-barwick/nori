package docstore

import (
	"context"
	"errors"
	"time"
)

type UsageCostRecord struct {
	CreatedAt     time.Time `firestore:"created_at"`
	Model         string    `firestore:"model"`
	Billing       string    `firestore:"billing"`
	Category      string    `firestore:"category"`
	CostUSD       *float64  `firestore:"cost_usd"`
	CostStatus    string    `firestore:"cost_status"`
	RequestStatus string    `firestore:"request_status"`
}

type UsageCostReader interface {
	ReadUsageCosts(context.Context, string, time.Time, time.Time, func(UsageCostRecord)) error
}

func (c *Client) ReadUsageCosts(ctx context.Context, path string, start, end time.Time, visit func(UsageCostRecord)) error {
	for backend := c.Backend; backend != nil; {
		if reader, ok := backend.(UsageCostReader); ok {
			return reader.ReadUsageCosts(ctx, path, start, end, visit)
		}
		wrapper, ok := backend.(BackendUnwrapper)
		if !ok {
			break
		}
		backend = wrapper.Unwrap()
	}
	query := Query{client: c, spec: QuerySpec{Collection: path}}
	rows := query.
		Where("created_at", ">=", start).Where("created_at", "<", end).
		Select("created_at", "model", "billing", "category", "cost_usd", "cost_status", "request_status").Documents(ctx)
	defer rows.Stop()
	for {
		row, err := rows.Next()
		if errors.Is(err, Done) {
			return nil
		}
		if err != nil {
			return err
		}
		var usage UsageCostRecord
		if err := row.DataTo(&usage); err != nil {
			return err
		}
		visit(usage)
	}
}
