// Package platform wires processes to their configured infrastructure. Nothing
// here has a default target: a process with missing configuration fails at
// startup instead of reaching a deployed project.
package platform

import (
	"context"
	"os"

	"nori/internal/computer"
	"nori/internal/computer/gce"
	"nori/internal/computer/local"
	"nori/internal/env"
	"nori/internal/state"
)

// OpenStore opens the durable store selected by this process's environment.
func OpenStore(ctx context.Context) (*state.Google, error) {
	project, err := env.Required("NORI_PROJECT_ID")
	if err != nil {
		return nil, err
	}
	database, err := env.Required("FIRESTORE_DATABASE_ID")
	if err != nil {
		return nil, err
	}
	return state.NewGoogle(ctx, project, database, os.Getenv("ARTIFACT_BUCKET"))
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
