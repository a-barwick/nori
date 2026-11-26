package rowstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// KeyRing implements KeyWrapper for zero-downtime master key rotation.
// Wrap always delegates to the primary KeyWrapper. Unwrap tries the primary
// first and then each fallback/historical KeyWrapper in order.
type KeyRing struct {
	primary   KeyWrapper
	fallbacks []KeyWrapper
}

var (
	_ KeyWrapper    = (*KeyRing)(nil)
	_ AADKeyWrapper = (*KeyRing)(nil)
)

// NewKeyRing constructs a KeyRing with a required primary KeyWrapper and zero
// or more fallback KeyWrappers used only during Unwrap.
func NewKeyRing(primary KeyWrapper, fallbacks ...KeyWrapper) (*KeyRing, error) {
	if primary == nil {
		return nil, errors.New("key ring requires a non-nil primary key wrapper")
	}
	copied := make([]KeyWrapper, 0, len(fallbacks))
	for i, fb := range fallbacks {
		if fb == nil {
			return nil, fmt.Errorf("key ring fallback at index %d must not be nil", i)
		}
		copied = append(copied, fb)
	}
	return &KeyRing{
		primary:   primary,
		fallbacks: copied,
	}, nil
}

// ParseKeyRing parses a comma-separated list of 32-byte master keys (each
// encoded as 64 hex characters or base64). The first key becomes the primary
// key used for Wrap; remaining keys are registered as fallbacks for Unwrap.
func ParseKeyRing(encoded string) (*KeyRing, error) {
	if strings.TrimSpace(encoded) == "" {
		return nil, errors.New("key ring must contain at least one master key")
	}
	parts := strings.Split(encoded, ",")
	wrappers := make([]KeyWrapper, 0, len(parts))
	for i, part := range parts {
		k, err := ParseMasterKey(part)
		if err != nil {
			return nil, fmt.Errorf("key ring entry %d: %w", i, err)
		}
		wrappers = append(wrappers, k)
	}
	return NewKeyRing(wrappers[0], wrappers[1:]...)
}

// Wrap encrypts dataKey using the primary KeyWrapper.
func (r *KeyRing) Wrap(ctx context.Context, dataKey []byte) ([]byte, string, error) {
	return r.WrapWithAAD(ctx, dataKey, nil)
}

// WrapWithAAD encrypts dataKey with aad using the primary KeyWrapper.
func (r *KeyRing) WrapWithAAD(ctx context.Context, dataKey, aad []byte) ([]byte, string, error) {
	if r == nil || r.primary == nil {
		return nil, "", errors.New("key ring has no primary key wrapper")
	}
	if len(aad) > 0 {
		if aw, ok := r.primary.(AADKeyWrapper); ok {
			return aw.WrapWithAAD(ctx, dataKey, aad)
		}
	}
	return r.primary.Wrap(ctx, dataKey)
}

// Unwrap attempts to decrypt wrapped using the primary KeyWrapper first, then
// each fallback KeyWrapper in order until one succeeds.
func (r *KeyRing) Unwrap(ctx context.Context, wrapped []byte, keyID string) ([]byte, error) {
	return r.UnwrapWithAAD(ctx, wrapped, keyID, nil)
}

// UnwrapWithAAD attempts to decrypt wrapped with aad using the primary
// KeyWrapper first, then each fallback KeyWrapper in order until one succeeds.
func (r *KeyRing) UnwrapWithAAD(ctx context.Context, wrapped []byte, keyID string, aad []byte) ([]byte, error) {
	if r == nil || r.primary == nil {
		return nil, errors.New("key ring has no primary key wrapper")
	}
	unwrapOne := func(w KeyWrapper) ([]byte, error) {
		if len(aad) > 0 {
			if aw, ok := w.(AADKeyWrapper); ok {
				return aw.UnwrapWithAAD(ctx, wrapped, keyID, aad)
			}
		}
		return w.Unwrap(ctx, wrapped, keyID)
	}
	dataKey, primaryErr := unwrapOne(r.primary)
	if primaryErr == nil {
		return dataKey, nil
	}
	for _, fb := range r.fallbacks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if dk, err := unwrapOne(fb); err == nil {
			return dk, nil
		}
	}
	return nil, fmt.Errorf("key %q could not be unwrapped by any key in ring: %w", keyID, primaryErr)
}
