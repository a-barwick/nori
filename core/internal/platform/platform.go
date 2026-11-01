// Package platform wires processes to their configured infrastructure. Nothing
// here has a default target: a process with missing configuration fails at
// startup instead of reaching a deployed project.
//
// This is the only production code that names a storage backend. Everything
// else depends on the contracts in internal/state and internal/spend.
package platform

import (
	"context"
	"fmt"
	"os"

	"nori/internal/computer"
	"nori/internal/computer/gce"
	"nori/internal/computer/local"
	"nori/internal/env"
	"nori/internal/state"
	statefirestore "nori/internal/state/firestore"
	"nori/internal/state/gcs"
	"nori/internal/state/localfs"
)

// StoreConfig selects the durable store a process opens.
type StoreConfig struct {
	Project  string
	Database string
	// ArtifactBucket holds artifact bytes in Cloud Storage. It is not required until an artifact is stored.
	ArtifactBucket string
}

// OpenStore opens the durable store selected by this process's environment.
func OpenStore(ctx context.Context) (state.ConversationStore, error) {
	project, err := env.Required("NORI_PROJECT_ID")
	if err != nil {
		return nil, err
	}
	database, err := env.Required("FIRESTORE_DATABASE_ID")
	if err != nil {
		return nil, err
	}
	return OpenStoreWith(ctx, StoreConfig{Project: project, Database: database, ArtifactBucket: os.Getenv("ARTIFACT_BUCKET")})
}

// OpenStoreWith opens documents in Firestore and artifact bytes in a local directory or Cloud Storage. The emulator
// and local-artifact environment guards apply to every process.
func OpenStoreWith(ctx context.Context, cfg StoreConfig) (state.ConversationStore, error) {
	local, err := openLocalArtifacts()
	if err != nil {
		return nil, err
	}
	closeLocal := func() {
		if local != nil {
			_ = local.Close()
		}
	}
	// An app-only emulator endpoint keeps development spend enforcement on its real shared database.
	// FIRESTORE_EMULATOR_HOST still supports unpaid tests.
	emulator := os.Getenv("NORI_STATE_EMULATOR_HOST")
	if emulator != "" && (!statefirestore.IsLoopback(emulator) || os.Getenv("FIRESTORE_EMULATOR_HOST") != "") {
		closeLocal()
		return nil, fmt.Errorf("NORI_STATE_EMULATOR_HOST requires loopback and no FIRESTORE_EMULATOR_HOST")
	}
	documents, err := statefirestore.Open(ctx, statefirestore.Config{Project: cfg.Project, Database: cfg.Database, EmulatorAddress: emulator})
	if err != nil {
		closeLocal()
		return nil, err
	}
	if local != nil {
		return state.Compose(documents, local), nil
	}
	objects, err := gcs.Open(ctx, cfg.ArtifactBucket)
	if err != nil {
		_ = documents.Close()
		return nil, err
	}
	return state.Compose(documents, objects), nil
}

// openLocalArtifacts opens the development artifact directory when NORI_LOCAL_ARTIFACT_DIR is set. It returns nil
// when it is not set. The directory is allowed only with a loopback Firestore emulator and local Temporal.
func openLocalArtifacts() (state.ArtifactStore, error) {
	directory := os.Getenv("NORI_LOCAL_ARTIFACT_DIR")
	if directory == "" {
		return nil, nil
	}
	address := os.Getenv("NORI_STATE_EMULATOR_HOST")
	if address == "" {
		address = os.Getenv("FIRESTORE_EMULATOR_HOST")
	}
	if !statefirestore.IsLoopback(address) || os.Getenv("TEMPORAL_LOCAL") != "true" {
		return nil, fmt.Errorf("NORI_LOCAL_ARTIFACT_DIR requires a loopback Firestore emulator and TEMPORAL_LOCAL=true")
	}
	store, err := localfs.Open(directory)
	if err != nil {
		return nil, fmt.Errorf("NORI_LOCAL_ARTIFACT_DIR %w", err)
	}
	return store, nil
}

// Computer returns the provider for this process's computer. NORI_LOCAL_COMPUTER_URL
// selects a loopback development computer. Otherwise the computer is a Compute
// Engine instance named by NORI_PROJECT_ID, COMPUTER_ZONE and COMPUTER_INSTANCE,
// none of which has a default. Connecting validates the choice, so a process that
// never uses the computer does not need one configured.
func Computer() computer.Provider {
	if address := os.Getenv("NORI_LOCAL_COMPUTER_URL"); address != "" {
		return computer.Traced(local.New(address))
	}
	return computer.Traced(gce.New(gce.Config{
		Project:  os.Getenv("NORI_PROJECT_ID"),
		Zone:     os.Getenv("COMPUTER_ZONE"),
		Instance: os.Getenv("COMPUTER_INSTANCE"),
	}))
}

// CheckComputer fails when the computer settings that Computer reads are incomplete: a local URL, or a project,
// zone and instance. A process that runs tools calls it at startup, so a bad configuration stops the process
// instead of failing every task. Computer stays lazy, so other processes need no computer settings.
func CheckComputer() error {
	if os.Getenv("NORI_LOCAL_COMPUTER_URL") != "" {
		return nil
	}
	for _, name := range []string{"NORI_PROJECT_ID", "COMPUTER_ZONE", "COMPUTER_INSTANCE"} {
		if _, err := env.Required(name); err != nil {
			return fmt.Errorf("computer is not configured: %w", err)
		}
	}
	return nil
}
