// Package platform wires processes to their configured infrastructure. Nothing
// here has a default target: a process with missing configuration fails at
// startup instead of reaching a deployed project.
//
// This is the only production code that names a storage backend. Everything
// else depends on the contracts in internal/state.
package platform

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"path/filepath"

	"nori/internal/computer"
	"nori/internal/computer/ec2"
	"nori/internal/computer/gce"
	"nori/internal/computer/local"
	"nori/internal/settings"
	"nori/internal/state"
	statefirestore "nori/internal/state/firestore"
	"nori/internal/state/gcs"
	"nori/internal/state/localfs"
	"nori/internal/state/postgres"
	"nori/internal/state/records"
	"nori/internal/state/s3"
	"nori/internal/usage"
	"nori/internal/users"
)

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
	var documents *records.Database
	cfg := s.Store
	switch cfg.Backend {
	case "", "firestore":
		if _, err = settings.Required("NORI_PROJECT_ID", cfg.Project); err != nil {
			closeLocal()
			return nil, err
		}
		if _, err = settings.Required("FIRESTORE_DATABASE_ID", cfg.Database); err != nil {
			closeLocal()
			return nil, err
		}
		emulator := cfg.StateEmulator
		if emulator != "" && (!statefirestore.IsLoopback(emulator) || cfg.FirestoreEmulator != "") {
			closeLocal()
			return nil, fmt.Errorf("NORI_STATE_EMULATOR_HOST requires loopback and no FIRESTORE_EMULATOR_HOST")
		}
		documents, err = statefirestore.Open(ctx, statefirestore.Config{Project: cfg.Project, Database: cfg.Database, EmulatorAddress: emulator}, user, s.TaskBudgetUSD)
	case "postgres":
		documents, err = postgres.Open(ctx, cfg.PostgresURL, user, s.TaskBudgetUSD)
	default:
		err = fmt.Errorf("NORI_STORE_BACKEND must be firestore or postgres")
	}
	if err != nil {
		closeLocal()
		return nil, err
	}
	if local != nil {
		return meteredStore(s, user, state.Compose(documents, local))
	}
	var objects state.ArtifactStore
	if s.Cloud == "aws" {
		objects, err = s3.Open(ctx, s.AWS.Region, cfg.ArtifactBucket, user)
	} else {
		objects, err = gcs.Open(ctx, cfg.ArtifactBucket, user)
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
		return computer.Traced(local.New(s.Computer.LocalURL))
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
