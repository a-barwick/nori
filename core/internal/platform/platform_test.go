package platform

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"nori/internal/settings"
	"nori/internal/state/gcs"
	"nori/internal/users"
)

func TestOpenObjectsSupportsGCPAndRejectsUnknownCloud(t *testing.T) {
	user := users.User{
		ID:       "11111111-1111-4111-8111-111111111111",
		Email:    "alice@example.com",
		TimeZone: "UTC",
	}

	storage := &Storage{
		gcs: gcs.NewClient(&http.Client{}, "https://storage.test/storage/v1", "https://storage.test/upload/storage/v1"),
	}

	store, err := storage.openObjects(context.Background(), settings.Settings{
		Cloud: "gcp",
		Store: settings.Store{ArtifactBucket: "nori-gcp-bucket"},
	}, user)
	if err != nil {
		t.Fatalf("openObjects(gcp): %v", err)
	}
	if _, ok := store.(*gcs.Store); !ok {
		t.Fatalf("expected *gcs.Store, got %T", store)
	}

	if _, err := storage.openObjects(context.Background(), settings.Settings{
		Cloud: "unknown",
	}, user); err == nil || !strings.Contains(err.Error(), "NORI_CLOUD=gcp") {
		t.Fatalf("expected error mentioning NORI_CLOUD=gcp, got %v", err)
	}

	if Secrets(settings.Settings{Cloud: "gcp"}) != nil {
		t.Fatalf("expected nil AWS secrets lookup when NORI_CLOUD=gcp")
	}
}
