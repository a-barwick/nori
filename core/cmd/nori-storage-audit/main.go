package main

import (
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"nori/internal/settings"
	"nori/internal/state/gcs"
	"nori/internal/state/postgres"
)

type gcsObjectStore struct {
	client *gcs.Client
}

func (g gcsObjectStore) ListObjects(ctx context.Context, bucket, prefix string) ([]Object, error) {
	items, err := g.client.ListObjects(ctx, bucket, prefix)
	if err != nil {
		return nil, err
	}
	out := make([]Object, 0, len(items))
	for _, item := range items {
		out = append(out, Object{
			Name:       item.Name,
			Generation: item.Generation,
			Size:       item.Size,
			Updated:    item.Updated,
		})
	}
	return out, nil
}

func (g gcsObjectStore) DeleteObject(ctx context.Context, bucket, name, generation string) error {
	return g.client.DeleteObject(ctx, bucket, name, generation)
}

func main() {
	if err := run(); err != nil {
		slog.Error("nori-storage-audit failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	bucketFlag := flag.String("bucket", "", "GCS bucket name (defaults to ARTIFACT_BUCKET)")
	graceFlag := flag.Duration("grace", DefaultGraceWindow, "minimum age before an unreferenced object is flagged as an orphan")
	userFlag := flag.String("user", "", "optional user UUID to audit exclusively")
	deleteFlag := flag.Bool("delete", false, "delete verified orphans (dry-run when false)")
	confirmFlag := flag.Bool("confirm", false, "required with --delete to authorize object deletion")
	maxDeleteFlag := flag.Int("max-delete", DefaultMaxDelete, "maximum number of orphan objects to delete in one run")
	jsonFlag := flag.Bool("json", false, "print machine-readable JSON report")
	timeoutFlag := flag.Duration("timeout", 15*time.Minute, "overall command timeout")
	flag.Parse()

	cfg, err := settings.LoadDefaultCloud("gcp")
	if err != nil {
		return err
	}
	bucket := cmp.Or(*bucketFlag, cfg.Store.ArtifactBucket)
	if bucket == "" {
		return fmt.Errorf("ARTIFACT_BUCKET or --bucket must be configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeoutFlag)
	defer cancel()

	backend, err := postgres.OpenBackendWithOptions(ctx, cfg.Store.PostgresURL, postgres.Options{
		MaxConns:          cfg.Store.PostgresMaxConns,
		MinConns:          cfg.Store.PostgresMinConns,
		MaxConnLifetime:   cfg.Store.PostgresMaxConnLifetime,
		MaxConnIdleTime:   cfg.Store.PostgresMaxConnIdleTime,
		HealthCheckPeriod: cfg.Store.PostgresHealthCheckPeriod,
		StatementTimeout:  cfg.Store.PostgresStatementTimeout,
	})
	if err != nil {
		return fmt.Errorf("open postgres: %w", err)
	}
	defer backend.Close()

	client, err := gcs.OpenClient(ctx)
	if err != nil {
		return fmt.Errorf("open gcs client: %w", err)
	}

	report, err := Reconcile(ctx, newPostgresReferenceStore(backend), gcsObjectStore{client: client}, Options{
		Bucket:      bucket,
		GraceWindow: *graceFlag,
		Delete:      *deleteFlag,
		Confirm:     *confirmFlag,
		MaxDelete:   *maxDeleteFlag,
		UserID:      *userFlag,
	})
	if err != nil {
		return err
	}

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	mode := "dry-run"
	if !report.DryRun {
		mode = "delete"
	}
	fmt.Printf("nori-storage-audit (%s) bucket=%s grace=%s\n", mode, report.Bucket, report.GraceWindow)
	fmt.Printf("users_scanned=%d users_skipped_locked=%d objects_scanned=%d\n",
		report.UsersScanned, report.UsersSkippedLocked, report.ObjectsScanned)
	fmt.Printf("orphans=%d orphan_bytes=%d", report.OrphanCount, report.OrphanBytes)
	if !report.OldestOrphan.IsZero() {
		fmt.Printf(" oldest=%s newest=%s", report.OldestOrphan.Format(time.RFC3339), report.NewestOrphan.Format(time.RFC3339))
	}
	fmt.Println()
	if !report.DryRun {
		fmt.Printf("deleted=%d deleted_bytes=%d cap_reached=%t\n",
			report.DeletedCount, report.DeletedBytes, report.DeleteCapReached)
	}
	return nil
}
