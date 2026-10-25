package task

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"strings"
	"time"

	"google.golang.org/api/secretmanager/v1"
)

// A deployed API can carry a Secret Manager version reference instead of a
// plaintext credential in its runtime configuration or Terraform state. Workers
// may continue using their platform's native TEMPORAL_API_KEY secret injection.
func apiKey(ctx context.Context) (string, error) {
	key := strings.TrimSpace(os.Getenv("TEMPORAL_API_KEY"))
	if version := strings.TrimSpace(os.Getenv("TEMPORAL_API_KEY_SECRET_VERSION")); version != "" {
		parts := strings.Split(version, "/")
		if len(parts) != 6 || parts[0] != "projects" || parts[1] == "" || parts[2] != "secrets" || parts[3] == "" || parts[4] != "versions" || parts[5] == "" {
			return "", errors.New("TEMPORAL_API_KEY_SECRET_VERSION must be projects/PROJECT/secrets/SECRET/versions/VERSION")
		}
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		secrets, err := secretmanager.NewService(ctx)
		if err != nil {
			return "", fmt.Errorf("initialize Temporal credential access: %w", err)
		}
		response, err := secrets.Projects.Secrets.Versions.Access(version).Context(ctx).Do()
		if err != nil {
			return "", fmt.Errorf("access Temporal credential: %w", err)
		}
		if response.Payload == nil {
			return "", errors.New("Temporal credential secret has no payload")
		}
		data, err := base64.StdEncoding.DecodeString(response.Payload.Data)
		if err != nil {
			return "", errors.New("Temporal credential secret has invalid encoding")
		}
		if int64(crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli))) != response.Payload.DataCrc32c {
			return "", errors.New("Temporal credential secret checksum mismatch")
		}
		key = strings.TrimSpace(string(data))
	}
	if key == "" {
		return "", errors.New("TEMPORAL_API_KEY or TEMPORAL_API_KEY_SECRET_VERSION is required for Temporal Cloud")
	}
	return key, nil
}
