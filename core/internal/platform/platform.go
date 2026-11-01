// Package platform wires processes to their configured infrastructure. Nothing
// here has a default target: a process with missing configuration fails at
// startup instead of reaching a deployed project.
package platform

import (
	"context"
	"os"

	"nori/internal/computer"
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

// Computer returns the configured computer target. Connecting validates it, so
// a process that never uses the computer does not need one configured.
func Computer() computer.Config {
	return computer.Config{
		Project:  os.Getenv("NORI_PROJECT_ID"),
		Zone:     os.Getenv("COMPUTER_ZONE"),
		Instance: os.Getenv("COMPUTER_INSTANCE"),
		LocalURL: os.Getenv("NORI_LOCAL_COMPUTER_URL"),
	}
}
