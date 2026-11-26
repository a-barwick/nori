package docstore

import (
	"context"
	"errors"
)

var ErrUserDataBusy = errors.New("Nori is finishing work for this account. Try again in a moment.")
var ErrUserDataResetPending = errors.New("Your data reset is incomplete. Return to Settings and try Reset my data again.")

type UserDataResetter interface {
	LockUserData(ctx context.Context, userID string, exclusive bool) (release func(), err error)
	BeginUserDataReset(ctx context.Context, userID string) error
	FinishUserDataReset(ctx context.Context, userID, owner string) error
	PublishUserDataReset(ctx context.Context, owner string)
}
