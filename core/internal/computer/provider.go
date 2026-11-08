package computer

import (
	"context"
	"errors"
)

// Provider is how callers reach the computer. An adapter implements it for one
// kind of infrastructure; callers never see which.
//
// Both methods return a client for the guest RPC and fail with ErrStopped only
// when the computer is verifiably stopped. The caller closes the client.
type Provider interface {
	// Open connects to a ready computer and starts it first if it is stopped,
	// which costs money. A stop that is already in progress finishes first.
	Open(ctx context.Context, run string) (*Client, error)
	// Attach connects to a computer that is already running or starting, for
	// cleanup. It never starts compute and returns ErrStopped when the
	// computer is stopped.
	Attach(ctx context.Context, run string) (*Client, error)
}

var errNoProvider = errors.New("computer provider is not configured")

func Open(ctx context.Context, p Provider, run string) (*Client, error) {
	if p == nil {
		return nil, errNoProvider
	}
	return p.Open(ctx, run)
}

func Attach(ctx context.Context, p Provider, run string) (*Client, error) {
	if p == nil {
		return nil, errNoProvider
	}
	return p.Attach(ctx, run)
}
