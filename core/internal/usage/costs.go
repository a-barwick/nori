package usage

import (
	"cmp"
	"context"
	"math"
	"slices"
	"sort"
	"time"

	"nori/internal/state/docstore"
)

type CostAmount struct {
	CostUSD       float64 `json:"cost_usd"`
	PricedCalls   int     `json:"priced_calls"`
	UnpricedCalls int     `json:"unpriced_calls"`
	LocalCalls    int     `json:"local_calls"`
	PendingCalls  int     `json:"pending_calls"`
}

type ModelCost struct {
	Model string `json:"model"`
	CostAmount
}

type CostBucket struct {
	StartAt time.Time   `json:"start_at"`
	EndAt   time.Time   `json:"end_at"`
	Totals  CostAmount  `json:"totals"`
	Models  []ModelCost `json:"models"`
}

type CostReport struct {
	Days       int          `json:"days"`
	Timezone   string       `json:"timezone"`
	StartAt    time.Time    `json:"start_at"`
	EndAt      time.Time    `json:"end_at"`
	BucketSize string       `json:"bucket_size"`
	Totals     CostAmount   `json:"totals"`
	Models     []ModelCost  `json:"models"`
	Buckets    []CostBucket `json:"buckets"`
}

func ReadCostReport(
	ctx context.Context,
	read func(context.Context, time.Time, time.Time, func(docstore.UsageCostRecord)) error,
	days int, zone *time.Location, now time.Time,
) (CostReport, error) {
	buckets := costBuckets(days, zone, now)
	report := CostReport{
		Days: days, Timezone: zone.String(), BucketSize: "day", Buckets: buckets,
		StartAt: buckets[0].StartAt, EndAt: buckets[len(buckets)-1].EndAt,
	}
	if days == 1 {
		report.BucketSize = "hour"
	}
	models := make([]map[string]*CostAmount, len(buckets))
	err := read(ctx, report.StartAt, now.Add(time.Nanosecond), func(row docstore.UsageCostRecord) {
		if row.RequestStatus == "not_sent" {
			return
		}
		i := sort.Search(len(buckets), func(i int) bool { return row.CreatedAt.Before(buckets[i].EndAt) })
		if i == len(buckets) || row.CreatedAt.Before(buckets[i].StartAt) {
			return
		}
		if models[i] == nil {
			models[i] = make(map[string]*CostAmount)
		}
		if models[i][row.Model] == nil {
			models[i][row.Model] = &CostAmount{}
		}
		amount := models[i][row.Model]
		switch {
		case row.RequestStatus == "pending":
			amount.PendingCalls++
		case row.CostStatus == "local":
			amount.LocalCalls++
		case row.CostUSD != nil && *row.CostUSD >= 0 && !math.IsInf(*row.CostUSD, 0) &&
			(*row.CostUSD > 0 || row.CostStatus == "provider" || row.CostStatus == "calculated"):
			amount.PricedCalls++
			amount.CostUSD += *row.CostUSD
		default:
			amount.UnpricedCalls++
		}
	})
	if err != nil {
		return CostReport{}, err
	}

	allModels := make(map[string]*CostAmount)
	for i := range buckets {
		bucket := &buckets[i]
		bucket.Models = sortedCosts(models[i])
		for _, model := range bucket.Models {
			bucket.Totals.add(model.CostAmount)
			if allModels[model.Model] == nil {
				allModels[model.Model] = &CostAmount{}
			}
			allModels[model.Model].add(model.CostAmount)
		}
		report.Totals.add(bucket.Totals)
	}
	report.Models = sortedCosts(allModels)
	return report, nil
}

func (a *CostAmount) add(b CostAmount) {
	a.CostUSD += b.CostUSD
	a.PricedCalls += b.PricedCalls
	a.UnpricedCalls += b.UnpricedCalls
	a.LocalCalls += b.LocalCalls
	a.PendingCalls += b.PendingCalls
}

func sortedCosts(models map[string]*CostAmount) []ModelCost {
	costs := make([]ModelCost, 0, len(models))
	for model, amount := range models {
		costs = append(costs, ModelCost{Model: model, CostAmount: *amount})
	}
	slices.SortFunc(costs, func(a, b ModelCost) int {
		if order := cmp.Compare(b.CostUSD, a.CostUSD); order != 0 {
			return order
		}
		return cmp.Compare(a.Model, b.Model)
	})
	return costs
}

func costBuckets(days int, zone *time.Location, now time.Time) []CostBucket {
	year, month, day := now.In(zone).Date()
	today := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	start := calendarDayStart(today.AddDate(0, 0, 1-days), zone)
	tomorrow := calendarDayStart(today.AddDate(0, 0, 1), zone)
	buckets := make([]CostBucket, 0, max(24, days))
	if days == 1 {
		for !now.Before(start) && start.Before(tomorrow) {
			local := start.In(zone)
			elapsed := time.Duration(local.Minute())*time.Minute + time.Duration(local.Second())*time.Second
			end := start.Add(time.Hour - elapsed)
			if _, transition := local.ZoneBounds(); !transition.IsZero() && transition.Before(end) {
				end = transition
			}
			buckets = append(buckets, CostBucket{StartAt: start.UTC(), EndAt: minTime(end, tomorrow).UTC()})
			start = end
		}
		return buckets
	}
	for day := 2 - days; day <= 1; day++ {
		end := calendarDayStart(today.AddDate(0, 0, day), zone)
		if start.Before(end) {
			buckets = append(buckets, CostBucket{StartAt: start.UTC(), EndAt: end.UTC()})
		}
		start = end
	}
	return buckets
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// A time zone transition can skip or repeat midnight. Find the first instant
// of the civil date by checking the UTC offset in each surrounding zone segment.
func calendarDayStart(date time.Time, zone *time.Location) time.Time {
	for cursor := date.Add(-24 * time.Hour).In(zone); ; {
		_, offset := cursor.Zone()
		candidate := date.Add(-time.Duration(offset) * time.Second)
		if candidate.Before(cursor) {
			candidate = cursor
		}
		_, transition := cursor.ZoneBounds()
		if transition.IsZero() || candidate.Before(transition) {
			return candidate
		}
		cursor = transition.In(zone)
	}
}
