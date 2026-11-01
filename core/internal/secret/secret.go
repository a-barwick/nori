// Package secret resolves credentials that a deployment keeps in a secret store
// instead of in plaintext configuration.
package secret

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Lookup returns the current value of the secret that ref names. The reference
// syntax belongs to the store behind it.
type Lookup interface {
	Secret(ctx context.Context, ref string) (string, error)
}

// FromEnv returns a credential from the environment. A reference in refVar
// takes precedence and is resolved with lookup; otherwise the plaintext in
// valueVar is used. The result is empty only when neither variable is set.
func FromEnv(ctx context.Context, lookup Lookup, valueVar, refVar string) (string, error) {
	value := strings.TrimSpace(os.Getenv(valueVar))
	ref := strings.TrimSpace(os.Getenv(refVar))
	if ref == "" {
		return value, nil
	}
	if lookup == nil {
		return "", errors.New(refVar + " is set but this process has no secret store")
	}
	resolved, err := lookup.Secret(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("%s: %w", refVar, err)
	}
	return strings.TrimSpace(resolved), nil
}
