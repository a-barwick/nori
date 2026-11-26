package docstore

import (
	"context"
	"errors"
)

var ErrUserDataBusy = errors.New("Nori is finishing work for this account. Try again in a moment.")
var ErrUserDataResetPending = errors.New("Your data reset is incomplete. Finish the reset to continue.")
var ErrUserDataResetNotPending = errors.New("There is no data reset to finish. Refresh your account.")

type UserDataResetter interface {
	LockUserData(ctx context.Context, userID string, exclusive bool) (release func(), err error)
	BeginUserDataReset(ctx context.Context, userID string) error
	FinishUserDataReset(ctx context.Context, userID, owner string) error
	PublishUserDataReset(ctx context.Context, owner string)
}
