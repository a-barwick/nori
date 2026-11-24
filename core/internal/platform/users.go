package platform

import (
	"context"

	"nori/internal/settings"
	"nori/internal/state/records"
	"nori/internal/users"
)

func OpenUsers(ctx context.Context, s settings.Settings) (users.Directory, error) {
	docs, err := openDocBackend(ctx, s.Store)
	if err != nil {
		return nil, err
	}
	return records.NewUsers(docs), nil
}

// ForUser combines process credentials with one row. It never modifies shared settings.
func ForUser(s settings.Settings, user users.User) settings.Settings {
	s.TimeZone = user.TimeZone
	return s
}
