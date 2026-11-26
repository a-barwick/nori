package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"nori/internal/users"
)

const (
	DefaultGraceWindow = 24 * time.Hour
	DefaultMaxDelete   = 1000
)

// Object represents a live blob returned from object storage.
type Object struct {
	Name       string
	Generation string
	Size       int64
	Updated    time.Time
}

// ObjectStore abstracts bucket listing and conditional deletion for testing.
type ObjectStore interface {
	ListObjects(ctx context.Context, bucket, prefix string) ([]Object, error)
	DeleteObject(ctx context.Context, bucket, name, generation string) error
}

// UserSnapshot captures a user's referenced artifact keys while holding any active data lock.
type UserSnapshot struct {
	User           users.User
	Locked         bool
	ReferencedKeys map[string]struct{}
}

// ReferenceStore abstracts Postgres user discovery and per-user reference inspection.
type ReferenceStore interface {
	ListUsers(ctx context.Context) ([]users.User, error)
	InspectUser(ctx context.Context, user users.User) (UserSnapshot, func(), error)
}

// Options configures an orphan audit or deletion pass.
type Options struct {
	Bucket      string
	GraceWindow time.Duration
	Delete      bool
	Confirm     bool
	MaxDelete   int
	UserID      string
	Now         func() time.Time
}

// Orphan describes one verified unreferenced object older than the grace window.
type Orphan struct {
	UserID     string    `json:"user_id"`
	Name       string    `json:"name"`
	Generation string    `json:"generation"`
	Size       int64     `json:"size"`
	Updated    time.Time `json:"updated"`
}

// Report summarizes the outcome of an orphan audit or deletion run.
type Report struct {
	Bucket             string    `json:"bucket"`
	DryRun             bool      `json:"dry_run"`
	GraceWindow        string    `json:"grace_window"`
	UsersScanned       int       `json:"users_scanned"`
	UsersSkippedLocked int       `json:"users_skipped_locked"`
	ObjectsScanned     int       `json:"objects_scanned"`
	OrphanCount        int       `json:"orphan_count"`
	OrphanBytes        int64     `json:"orphan_bytes"`
	OldestOrphan       time.Time `json:"oldest_orphan,omitempty"`
	NewestOrphan       time.Time `json:"newest_orphan,omitempty"`
	DeletedCount       int       `json:"deleted_count"`
	DeletedBytes       int64     `json:"deleted_bytes"`
	DeleteCapReached   bool      `json:"delete_cap_reached,omitempty"`
	Orphans            []Orphan  `json:"orphans,omitempty"`
}

// Reconcile scans per-user prefixes under artifacts/users/<id>/ and compares
// live blobs against Postgres references. Dry-run by default.
func Reconcile(ctx context.Context, refs ReferenceStore, store ObjectStore, opts Options) (Report, error) {
	if strings.TrimSpace(opts.Bucket) == "" {
		return Report{}, errors.New("bucket must be configured")
	}
	if opts.GraceWindow <= 0 {
		opts.GraceWindow = DefaultGraceWindow
	}
	if opts.Delete {
		if !opts.Confirm {
			return Report{}, errors.New("--delete requires --confirm to prevent accidental deletion")
		}
		if opts.MaxDelete <= 0 {
			return Report{}, errors.New("--max-delete must be a positive integer when --delete is set")
		}
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	now := nowFn().UTC()
	cutoff := now.Add(-opts.GraceWindow)

	report := Report{
		Bucket:      opts.Bucket,
		DryRun:      !opts.Delete,
		GraceWindow: opts.GraceWindow.String(),
	}

	allUsers, err := refs.ListUsers(ctx)
	if err != nil {
		return report, fmt.Errorf("list users: %w", err)
	}

	for _, user := range allUsers {
		if user.ID == users.LegacyID {
			continue
		}
		if opts.UserID != "" && user.ID != opts.UserID {
			continue
		}
		if err := user.Validate(); err != nil {
			return report, fmt.Errorf("invalid user %q: %w", user.ID, err)
		}
		if user.ResetPending {
			report.UsersSkippedLocked++
			continue
		}

		snap, release, err := refs.InspectUser(ctx, user)
		if err != nil {
			return report, fmt.Errorf("inspect user %s: %w", user.ID, err)
		}
		if snap.Locked {
			if release != nil {
				release()
			}
			report.UsersSkippedLocked++
			continue
		}

		err = func() error {
			if release != nil {
				defer release()
			}
			report.UsersScanned++

			prefix := "artifacts/" + user.FilePrefix()
			if !strings.HasPrefix(prefix, "artifacts/users/") || !strings.HasSuffix(prefix, "/") {
				return fmt.Errorf("unexpected user artifact prefix %q", prefix)
			}
			objects, err := store.ListObjects(ctx, opts.Bucket, prefix)
			if err != nil {
				return fmt.Errorf("list objects for user %s: %w", user.ID, err)
			}
			for _, obj := range objects {
				if !strings.HasPrefix(obj.Name, prefix) {
					return fmt.Errorf("object listing for user %s returned foreign key %q", user.ID, obj.Name)
				}
				report.ObjectsScanned++
				relKey := strings.TrimPrefix(obj.Name, prefix)
				if _, referenced := snap.ReferencedKeys[relKey]; referenced {
					continue
				}
				if obj.Updated.IsZero() || !obj.Updated.Before(cutoff) {
					continue
				}

				orphan := Orphan{
					UserID:     user.ID,
					Name:       obj.Name,
					Generation: obj.Generation,
					Size:       obj.Size,
					Updated:    obj.Updated.UTC(),
				}
				report.OrphanCount++
				report.OrphanBytes += obj.Size
				if report.OldestOrphan.IsZero() || orphan.Updated.Before(report.OldestOrphan) {
					report.OldestOrphan = orphan.Updated
				}
				if report.NewestOrphan.IsZero() || orphan.Updated.After(report.NewestOrphan) {
					report.NewestOrphan = orphan.Updated
				}
				report.Orphans = append(report.Orphans, orphan)

				if opts.Delete {
					if report.DeletedCount >= opts.MaxDelete {
						report.DeleteCapReached = true
						continue
					}
					if !strings.HasPrefix(orphan.Name, prefix) {
						return fmt.Errorf("refusing to delete key %q outside user prefix %q", orphan.Name, prefix)
					}
					if err := store.DeleteObject(ctx, opts.Bucket, orphan.Name, orphan.Generation); err != nil {
						return fmt.Errorf("delete orphan %q: %w", orphan.Name, err)
					}
					report.DeletedCount++
					report.DeletedBytes += orphan.Size
				}
			}
			return nil
		}()
		if err != nil {
			return report, err
		}
	}

	return report, nil
}
