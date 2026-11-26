package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"nori/internal/state/docstore"
)

const usageWindowSQL = `
SELECT count(*) FILTER (WHERE idx_created_at >= $3::jsonb),
       coalesce(sum(cost) FILTER (WHERE idx_created_at >= $4::jsonb AND cost > 0), 0),
       coalesce(sum(coalesce(nullif(exposure, 0), $5)) FILTER (WHERE idx_created_at >= $4::jsonb AND unknown), 0),
       count(*) FILTER (WHERE idx_created_at >= $4::jsonb AND unknown)
FROM (
	SELECT idx_created_at,
	       (data->>'cost_usd')::double precision AS cost,
	       greatest((data->>'estimated_exposure_usd')::double precision, 0) AS exposure,
	       coalesce(data->>'cost_status','') NOT IN ('provider', 'calculated', 'local')
	         AND coalesce((data->>'cost_usd')::double precision, 0) <= 0 AS unknown
	FROM nori_records
	WHERE shard = $1 AND collection = $2 AND idx_created_at >= least($3::jsonb, $4::jsonb)
	  AND data->>'request_status' IS DISTINCT FROM 'not_sent'
) usage
`

func readUsageWindow(ctx context.Context, q executor, collection string, hour, day time.Time, unknown float64) (docstore.UsageWindow, error) {
	hourValue, err := json.Marshal(docstore.JSONValue(hour))
	if err != nil {
		return docstore.UsageWindow{}, err
	}
	dayValue, err := json.Marshal(docstore.JSONValue(day))
	if err != nil {
		return docstore.UsageWindow{}, err
	}
	var window docstore.UsageWindow
	err = q.QueryRow(ctx, usageWindowSQL, recordShard(collection), collection, hourValue, dayValue, unknown).Scan(
		&window.Calls, &window.ActualUSD, &window.ExposureUSD, &window.Unknown)
	return window, err
}

func (b *backend) ReadUsageWindow(ctx context.Context, collection string, hour, day time.Time, unknown float64) (docstore.UsageWindow, error) {
	return readUsageWindow(ctx, b.pool, collection, hour, day, unknown)
}

func (t *transaction) ReadUsageWindow(ctx context.Context, collection string, hour, day time.Time, unknown float64) (docstore.UsageWindow, error) {
	if err := t.ensureTx(ctx, recordShard(collection)); err != nil {
		return docstore.UsageWindow{}, err
	}
	return readUsageWindow(ctx, t.q, collection, hour, day, unknown)
}

const usageCostsSQL = `
SELECT idx_created_at #>> '{}', coalesce(data->>'model', ''),
       (data->>'cost_usd')::double precision, coalesce(data->>'cost_status', ''),
       coalesce(data->>'request_status', '')
FROM nori_records
WHERE shard = $1 AND collection = $2
  AND idx_created_at >= to_jsonb($3::text) AND idx_created_at < to_jsonb($4::text)
  AND data->>'request_status' IS DISTINCT FROM 'not_sent'
`

func (b *backend) ReadUsageCosts(ctx context.Context, collection string, start, end time.Time, visit func(docstore.UsageCostRecord)) error {
	rows, err := b.pool.Query(ctx, usageCostsSQL, recordShard(collection), collection, docstore.JSONValue(start), docstore.JSONValue(end))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var record docstore.UsageCostRecord
		var createdAt string
		if err := rows.Scan(&createdAt, &record.Model, &record.CostUSD, &record.CostStatus, &record.RequestStatus); err != nil {
			return err
		}
		record.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return fmt.Errorf("decode model usage timestamp: %w", err)
		}
		visit(record)
	}
	return rows.Err()
}
