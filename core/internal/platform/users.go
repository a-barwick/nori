package platform

import (
	"context"
	"fmt"

	"nori/internal/settings"
	statefirestore "nori/internal/state/firestore"
	"nori/internal/users"
)

func OpenUsers(ctx context.Context, s settings.Settings) (users.Directory, error) {
	if _, err := settings.Required("NORI_PROJECT_ID", s.Store.Project); err != nil {
		return nil, err
	}
	if _, err := settings.Required("FIRESTORE_DATABASE_ID", s.Store.Database); err != nil {
		return nil, err
	}
	if s.Store.StateEmulator != "" && s.Store.FirestoreEmulator != "" {
		return nil, fmt.Errorf("NORI_STATE_EMULATOR_HOST and FIRESTORE_EMULATOR_HOST cannot both be set")
	}
	return statefirestore.OpenUsers(ctx, statefirestore.Config{Project: s.Store.Project, Database: s.Store.Database, EmulatorAddress: s.Store.StateEmulator, TaskBudgetUSD: s.TaskBudgetUSD})
}

// ForUser combines process credentials with one row. It never modifies shared settings.
func ForUser(s settings.Settings, user users.User) settings.Settings {
	s.TimeZone, s.NotifyEmail = user.TimeZone, user.NotifyEmail
	s.Calendar.RefreshToken, s.Calendar.Account, s.Calendar.WriteID = user.Calendar.RefreshToken, user.Calendar.Account, user.Calendar.WriteID
	s.Computer = settings.Computer{Zone: user.Computer.Zone, Instance: user.Computer.Instance, LocalURL: user.Computer.LocalURL}
	return s
}
