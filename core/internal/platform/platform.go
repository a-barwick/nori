// Package platform wires processes to their configured infrastructure. Nothing
// here has a default target: a process with missing configuration fails at
// startup instead of reaching a deployed project.
//
// This is the only production code that names a storage backend. Everything
// else depends on the contracts in internal/state.
package platform

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"sync"

	"nori/internal/computer"
	"nori/internal/computer/ec2"
	"nori/internal/computer/gce"
	"nori/internal/computer/local"
	"nori/internal/settings"
	"nori/internal/state"
	"nori/internal/state/docstore"
	statefirestore "nori/internal/state/firestore"
	"nori/internal/state/gcs"
	"nori/internal/state/localfs"
	"nori/internal/state/postgres"
	"nori/internal/state/records"
	"nori/internal/state/s3"
	"nori/internal/usage"
	"nori/internal/users"
)

// Storage holds one process-wide document backend and cloud object client so
// per-user scopes share connection pools instead of opening one per user.
type Storage struct {
	docs docstore.Backend
	mu   sync.Mutex
	gcs  *gcs.Client
	s3   *s3.Client
}

func OpenStorage(ctx context.Context, s settings.Settings) (*Storage, error) {
	docs, err := openDocBackend(ctx, s.Store)
	if err != nil {
		return nil, err
	}
	return &Storage{docs: docs}, nil
}

func (p *Storage) Users() users.Directory {
	return records.NewSharedUsers(p.docs)
}

func (p *Storage) OpenStore(ctx context.Context, s settings.Settings, user users.User) (state.ConversationStore, error) {
	if err := user.Validate(); err != nil {
		return nil, err
	}
	local, err := openLocalArtifacts(s, user)
	if err != nil {
		return nil, err
	}
	documents := records.NewShared(p.docs, user, s.TaskBudgetUSD)
	if local != nil {
		return meteredStore(s, user, state.Compose(documents, local))
	}
	objects, err := p.openObjects(ctx, s, user)
	if err != nil {
		return nil, err
	}
	return meteredStore(s, user, state.Compose(documents, objects))
}

func (p *Storage) openObjects(ctx context.Context, s settings.Settings, user users.User) (state.ArtifactStore, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if s.Cloud == "aws" {
		if p.s3 == nil {
			c, err := s3.OpenClient(ctx, s.AWS.Region)
			if err != nil {
				return nil, err
			}
			p.s3 = c
		}
		return p.s3.ForUser(s.Store.ArtifactBucket, user)
	}
	if p.gcs == nil {
		c, err := gcs.OpenClient(ctx)
		if err != nil {
			return nil, err
		}
		p.gcs = c
	}
	return p.gcs.ForUser(s.Store.ArtifactBucket, user)
}

func (p *Storage) Close() error {
	p.mu.Lock()
	gcsClient := p.gcs
	p.gcs = nil
	p.mu.Unlock()
	var gcsErr error
	if gcsClient != nil {
		gcsErr = gcsClient.Close()
	}
	return errors.Join(gcsErr, p.docs.Close())
}

func openDocBackend(ctx context.Context, cfg settings.Store) (docstore.Backend, error) {
	switch cfg.Backend {
	case "", "firestore":
		if _, err := settings.Required("NORI_PROJECT_ID", cfg.Project); err != nil {
			return nil, err
		}
		if _, err := settings.Required("FIRESTORE_DATABASE_ID", cfg.Database); err != nil {
			return nil, err
		}
		emulator := cfg.StateEmulator
		if emulator != "" && (!statefirestore.IsLoopback(emulator) || cfg.FirestoreEmulator != "") {
			return nil, fmt.Errorf("NORI_STATE_EMULATOR_HOST requires loopback and no FIRESTORE_EMULATOR_HOST")
		}
		return statefirestore.OpenBackend(ctx, statefirestore.Config{Project: cfg.Project, Database: cfg.Database, EmulatorAddress: emulator})
	case "postgres":
		return postgres.OpenBackend(ctx, cfg.PostgresURL)
	default:
		return nil, fmt.Errorf("NORI_STORE_BACKEND must be firestore or postgres")
	}
}

func OpenStore(ctx context.Context, s settings.Settings, user users.User) (state.ConversationStore, error) {
	if err := user.Validate(); err != nil {
		return nil, err
	}
	local, err := openLocalArtifacts(s, user)
	if err != nil {
		return nil, err
	}
	closeLocal := func() {
		if local != nil {
			_ = local.Close()
		}
	}
	docs, err := openDocBackend(ctx, s.Store)
	if err != nil {
		closeLocal()
		return nil, err
	}
	documents := records.New(docs, user, s.TaskBudgetUSD)
	if local != nil {
		return meteredStore(s, user, state.Compose(documents, local))
	}
	var objects state.ArtifactStore
	if s.Cloud == "aws" {
		objects, err = s3.Open(ctx, s.AWS.Region, s.Store.ArtifactBucket, user)
	} else {
		objects, err = gcs.Open(ctx, s.Store.ArtifactBucket, user)
	}
	if err != nil {
		_ = documents.Close()
		return nil, err
	}
	return meteredStore(s, user, state.Compose(documents, objects))
}

// openLocalArtifacts opens the development artifact directory when NORI_LOCAL_ARTIFACT_DIR is set. It returns nil
// when it is not set. The directory is allowed only with a loopback database and local Temporal.
func openLocalArtifacts(s settings.Settings, user users.User) (state.ArtifactStore, error) {
	directory := s.Store.LocalArtifactDir
	if directory == "" {
		if s.Cloud == "local" {
			return nil, fmt.Errorf("NORI_CLOUD=local requires NORI_LOCAL_ARTIFACT_DIR")
		}
		return nil, nil
	}
	address := s.Store.StateEmulator
	if address == "" {
		address = s.Store.FirestoreEmulator
	}
	localDatabase := statefirestore.IsLoopback(address)
	if s.Store.Backend == "postgres" {
		u, e := url.Parse(s.Store.PostgresURL)
		localDatabase = e == nil && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback())
	}
	if !localDatabase || (!s.Temporal.Local && !s.Features.DisableBackgroundTasks) {
		return nil, fmt.Errorf("NORI_LOCAL_ARTIFACT_DIR requires a loopback database and either chat-only mode or TEMPORAL_LOCAL=true")
	}
	store, err := localfs.Open(filepath.Join(directory, user.FilePrefix()))
	if err != nil {
		return nil, fmt.Errorf("NORI_LOCAL_ARTIFACT_DIR %w", err)
	}
	return store, nil
}

// Computer returns the provider for the selected user. A local URL selects a
// loopback development computer; otherwise the cloud setting selects a retained GCE or EC2 instance. There is no default computer. Connecting validates the choice, so a process that
// never uses the computer does not need one configured.
func Computer(s settings.Settings) computer.Provider {
	if s.Features.DisableComputer {
		return nil
	}
	if address := s.Computer.LocalURL; address != "" {
		return computer.Traced(local.New(address))
	}
	if s.Cloud == "local" {
		// A local user without a computer URL has no computer: tools that need one are not offered and cleanup
		// treats it as stopped.
		return nil
	}
	if s.Cloud == "aws" {
		return computer.Traced(ec2.New(ec2.Config{Region: s.AWS.Region, Instance: s.Computer.Instance}))
	}
	return computer.Traced(gce.New(gce.Config{
		Project:  s.Store.Project,
		Zone:     s.Computer.Zone,
		Instance: s.Computer.Instance,
	}))
}

// CheckComputer fails when the computer settings that Computer reads are incomplete: a local URL, or a project,
// zone and instance on GCP, or AWS region and instance. A process that runs tools calls it at startup, so a bad configuration stops the process
// instead of failing every task. Computer stays lazy, so other processes need no computer settings.
func CheckComputer(s settings.Settings) error {
	if s.Features.DisableComputer {
		return nil
	}
	if s.Computer.LocalURL != "" {
		if !s.Temporal.Local {
			return fmt.Errorf("user computer local URL requires TEMPORAL_LOCAL=true")
		}
		if err := local.Validate(s.Computer.LocalURL); err != nil {
			return fmt.Errorf("user computer local URL: %w", err)
		}
		return nil
	}
	required := [][2]string{{"NORI_PROJECT_ID", s.Store.Project}, {"user computer zone", s.Computer.Zone}, {"user computer instance", s.Computer.Instance}}
	if s.Cloud == "local" {
		return fmt.Errorf("local cloud requires a user computer local URL")
	}
	if s.Cloud == "aws" {
		required = [][2]string{{"AWS_REGION", s.AWS.Region}, {"user computer instance", s.Computer.Instance}}
	}
	for _, setting := range required {
		if _, err := settings.Required(setting[0], setting[1]); err != nil {
			return fmt.Errorf("computer is not configured: %w", err)
		}
	}
	return nil
}

func meteredStore(s settings.Settings, user users.User, store state.ConversationStore) (state.ConversationStore, error) {
	meter, err := usage.Open(s.Metering, user.ID, store)
	if err != nil {
		store.Close()
		return nil, err
	}
	return usage.Wrap(store, meter), nil
}
