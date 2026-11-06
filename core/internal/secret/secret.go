// Package secret resolves credentials that a deployment keeps in a secret store
// instead of in plaintext configuration.
package secret

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Lookup returns the current value of the secret that ref names. The reference
// syntax belongs to the store behind it.
type Lookup interface {
	Secret(ctx context.Context, ref string) (string, error)
}

// Resolve returns a credential from its settings. A reference takes precedence and is resolved with lookup;
// otherwise the plaintext value is used. The result is empty only when neither is set. refVar names the
// reference's setting in errors.
func Resolve(ctx context.Context, lookup Lookup, value, ref, refVar string) (string, error) {
	value, ref = strings.TrimSpace(value), strings.TrimSpace(ref)
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
