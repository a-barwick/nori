package secret

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var bearerTokenPattern = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+=*$`)

// ValidBearerToken reports whether value uses only URL-safe/RFC6750 bearer token characters.
func ValidBearerToken(value string) bool {
	return bearerTokenPattern.MatchString(value)
}

type Lookup interface {
	Secret(ctx context.Context, ref string) (string, error)
}

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
