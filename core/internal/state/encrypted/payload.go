package encrypted

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// PayloadPrefix tags encrypted string fields so legacy plaintext strings pass
// through transparently on read.
const PayloadPrefix = "nori:enc:v1:"

// PayloadCodec encrypts and decrypts individual string fields with AAD bound
// to a logical scope and record ID.
type PayloadCodec struct {
	Sealer Sealer
}

// NewPayloadCodec constructs a PayloadCodec backed by sealer.
func NewPayloadCodec(sealer Sealer) (*PayloadCodec, error) {
	if sealer == nil {
		return nil, errors.New("payload codec requires a sealer")
	}
	return &PayloadCodec{Sealer: sealer}, nil
}

// IsEncrypted reports whether value carries the PayloadPrefix tag.
func IsEncrypted(value string) bool {
	return strings.HasPrefix(value, PayloadPrefix)
}

// EncryptField encrypts plaintext with AAD bound to scope and id, returning a
// tagged base64 ciphertext string.
func (c *PayloadCodec) EncryptField(ctx context.Context, scope, id, plaintext string) (string, error) {
	if c == nil || c.Sealer == nil {
		return "", errors.New("payload codec has no sealer")
	}
	ct, err := c.Sealer.Seal(ctx, scope, id, []byte(plaintext))
	if err != nil {
		return "", err
	}
	return PayloadPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

// DecryptField decrypts a tagged ciphertext produced by EncryptField. Values
// without PayloadPrefix are treated as legacy plaintext and returned unchanged.
func (c *PayloadCodec) DecryptField(ctx context.Context, scope, id, value string) (string, error) {
	if !strings.HasPrefix(value, PayloadPrefix) {
		return value, nil
	}
	if c == nil || c.Sealer == nil {
		return "", errors.New("payload codec has no sealer")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, PayloadPrefix))
	if err != nil {
		return "", fmt.Errorf("invalid encrypted payload encoding: %w", err)
	}
	pt, err := c.Sealer.Open(ctx, scope, id, raw)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
