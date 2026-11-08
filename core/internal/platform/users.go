package platform

import (
	"context"
	"fmt"

	"nori/internal/settings"
	statefirestore "nori/internal/state/firestore"
	"nori/internal/state/postgres"
	"nori/internal/users"
)

func OpenUsers(ctx context.Context, s settings.Settings) (users.Directory, error) {
	if s.Store.Backend == "postgres" {
		return postgres.OpenUsers(ctx, s.Store.PostgresURL)
	}
	if s.Store.Backend != "" && s.Store.Backend != "firestore" {
		return nil, fmt.Errorf("NORI_STORE_BACKEND must be firestore or postgres")
	}
	if _, err := settings.Required("NORI_PROJECT_ID", s.Store.Project); err != nil {
		return nil, err
	}
	if _, err := settings.Required("FIRESTORE_DATABASE_ID", s.Store.Database); err != nil {
		return nil, err
	}
	if s.Store.StateEmulator != "" && s.Store.FirestoreEmulator != "" {
		return nil, fmt.Errorf("NORI_STATE_EMULATOR_HOST and FIRESTORE_EMULATOR_HOST cannot both be set")
	}
	return statefirestore.OpenUsers(ctx, statefirestore.Config{Project: s.Store.Project, Database: s.Store.Database, EmulatorAddress: s.Store.StateEmulator})
}

// ForUser combines process credentials with one row. It never modifies shared settings.
func ForUser(s settings.Settings, user users.User) settings.Settings {
	s.TimeZone = user.TimeZone
	s.Computer = settings.Computer{Zone: user.Computer.Zone, Instance: user.Computer.Instance, LocalURL: user.Computer.LocalURL}
	return s
}
