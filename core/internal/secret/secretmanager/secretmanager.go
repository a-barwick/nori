// Package secretmanager reads secrets from Google Secret Manager.
package secretmanager

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/crc32"
	"strings"
	"time"

	sm "google.golang.org/api/secretmanager/v1"
	"nori/internal/secret"
)

// Lookup resolves projects/PROJECT/secrets/SECRET/versions/VERSION references
// with Application Default Credentials. The zero value is ready to use and
// opens no connection until the first lookup.
type Lookup struct{}

var _ secret.Lookup = Lookup{}

func (Lookup) Secret(ctx context.Context, version string) (string, error) {
	parts := strings.Split(version, "/")
	if len(parts) != 6 || parts[0] != "projects" || parts[1] == "" || parts[2] != "secrets" || parts[3] == "" ||
		parts[4] != "versions" || parts[5] == "" {
		return "", errors.New("must be projects/PROJECT/secrets/SECRET/versions/VERSION")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	secrets, err := sm.NewService(ctx)
	if err != nil {
		return "", fmt.Errorf("initialize secret access: %w", err)
	}
	response, err := secrets.Projects.Secrets.Versions.Access(version).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("access secret: %w", err)
	}
	if response.Payload == nil {
		return "", errors.New("secret has no payload")
	}
	data, err := base64.StdEncoding.DecodeString(response.Payload.Data)
	if err != nil {
		return "", errors.New("secret has invalid encoding")
	}
	if int64(crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli))) != response.Payload.DataCrc32c {
		return "", errors.New("secret checksum mismatch")
	}
	return string(data), nil
}
