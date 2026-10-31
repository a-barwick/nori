package env

import (
	"context"
	"nori/internal/workerwake"
	"os"
)

// WorkerWake reads worker delivery configuration once at process startup.
func WorkerWake(ctx context.Context) (*workerwake.Cloud, error) {
	return workerwake.NewCloud(ctx, os.Getenv("NORI_WORKER_SERVICES"), os.Getenv("NORI_WORKER_WAKE_QUEUE"), os.Getenv("NORI_WAKE_SERVICE_ACCOUNT"), os.Getenv("CLOUD_TASKS_EMULATOR_HOST"), os.Getenv("TEMPORAL_LOCAL") == "true")
}
