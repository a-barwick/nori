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
	"nori/internal/computer/gce"
	"nori/internal/computer/local"
	"nori/internal/settings"
	"nori/internal/state"
	statefirestore "nori/internal/state/firestore"
	"nori/internal/state/gcs"
	"nori/internal/state/localfs"
	"nori/internal/state/postgres"
	"nori/internal/state/records"
	"nori/internal/usage"
	"nori/internal/users"
)

// StoreConfig selects the durable store a process opens.
type StoreConfig struct {
	Project  string
	Database string
	// ArtifactBucket holds artifact bytes in Cloud Storage. It is not required until an artifact is stored.
	ArtifactBucket string
}

// OpenStore opens the durable store selected by this process's settings.
func OpenStore(ctx context.Context, s settings.Settings, user users.User) (state.ConversationStore, error) {
	return OpenStoreWith(ctx, s, StoreConfig{Project: s.Store.Project, Database: s.Store.Database, ArtifactBucket: s.Store.ArtifactBucket}, user)
}

// OpenStoreWith opens the selected document backend and artifact bytes in a local directory or Cloud Storage. The emulator
// and local-artifact guards in s apply to every process.
func OpenStoreWith(ctx context.Context, s settings.Settings, cfg StoreConfig, user users.User) (state.ConversationStore, error) {
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
	switch s.Store.Backend {
	case "", "firestore":
		if _, err = settings.Required("NORI_PROJECT_ID", cfg.Project); err != nil {
			closeLocal()
			return nil, err
		}
		if _, err = settings.Required("FIRESTORE_DATABASE_ID", cfg.Database); err != nil {
			closeLocal()
			return nil, err
		}
		emulator := s.Store.StateEmulator
		if emulator != "" && (!statefirestore.IsLoopback(emulator) || s.Store.FirestoreEmulator != "") {
			closeLocal()
			return nil, fmt.Errorf("NORI_STATE_EMULATOR_HOST requires loopback and no FIRESTORE_EMULATOR_HOST")
		}
		documents, err = statefirestore.Open(ctx, statefirestore.Config{Project: cfg.Project, Database: cfg.Database, EmulatorAddress: emulator, TaskBudgetUSD: s.TaskBudgetUSD})
	case "postgres":
		documents, err = postgres.Open(ctx, postgres.Config{URL: s.Store.PostgresURL, TaskBudgetUSD: s.TaskBudgetUSD})
	default:
		err = fmt.Errorf("NORI_STORE_BACKEND must be firestore or postgres")
	}
	if err != nil {
		closeLocal()
		return nil, err
	}
	documents, err = documents.ForUser(user)
	if err != nil {
		closeLocal()
		return nil, err
	}
	if local != nil {
		return meteredStore(s, user, state.Compose(documents, local))
	}
	objects, err := gcs.Open(ctx, cfg.ArtifactBucket, user)
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
	if !localDatabase || !s.Temporal.Local {
		return nil, fmt.Errorf("NORI_LOCAL_ARTIFACT_DIR requires a loopback database and TEMPORAL_LOCAL=true")
	}
	store, err := localfs.Open(filepath.Join(directory, user.FilePrefix()))
	if err != nil {
		return nil, fmt.Errorf("NORI_LOCAL_ARTIFACT_DIR %w", err)
	}
	return store, nil
}

// Computer returns the provider for the selected user. A local URL selects a
// loopback development computer; otherwise the row names a Compute Engine
// instance in NORI_PROJECT_ID. There is no default computer. Connecting validates the choice, so a process that
// never uses the computer does not need one configured.
func Computer(s settings.Settings) computer.Provider {
	if address := s.Computer.LocalURL; address != "" {
		return computer.Traced(local.New(address))
	}
	return computer.Traced(gce.New(gce.Config{
		Project:  s.Store.Project,
		Zone:     s.Computer.Zone,
		Instance: s.Computer.Instance,
	}))
}

// CheckComputer fails when the computer settings that Computer reads are incomplete: a local URL, or a project,
// zone and instance. A process that runs tools calls it at startup, so a bad configuration stops the process
// instead of failing every task. Computer stays lazy, so other processes need no computer settings.
func CheckComputer(s settings.Settings) error {
	if s.Computer.LocalURL != "" {
		if !s.Temporal.Local {
			return fmt.Errorf("user computer local URL requires TEMPORAL_LOCAL=true")
		}
		if err := local.Validate(s.Computer.LocalURL); err != nil {
			return fmt.Errorf("user computer local URL: %w", err)
		}
		return nil
	}
	for _, setting := range [][2]string{{"NORI_PROJECT_ID", s.Store.Project}, {"user computer zone", s.Computer.Zone}, {"user computer instance", s.Computer.Instance}} {
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
