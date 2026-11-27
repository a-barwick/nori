package settings

import (
	"slices"
	"strings"
	"testing"
)

func TestComputerSettingsValidation(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		clearModelEnv(t)
		t.Setenv("NORI_CLOUD", "local")
		t.Setenv("NORI_LOCAL_ARTIFACT_DIR", t.TempDir())
		t.Setenv("NORI_POSTGRES_URL", "postgres://nori@127.0.0.1:15432/nori?sslmode=disable")

		s, err := LoadDefaultCloud("local")
		if err != nil {
			t.Fatalf("LoadDefaultCloud: %v", err)
		}
		if s.Computer.Enabled() || s.Computer.Provider != "disabled" {
			t.Fatalf("expected disabled computer, got %+v", s.Computer)
		}
	})

	t.Run("sprites requires token and redacts it", func(t *testing.T) {
		clearModelEnv(t)
		t.Setenv("NORI_CLOUD", "gcp")
		t.Setenv("ARTIFACT_BUCKET", "nori-test-bucket")
		t.Setenv("NORI_COMPUTER_PROVIDER", "sprites")
		t.Setenv("NORI_SPRITES_API_TOKEN", "")

		if _, err := LoadDefaultCloud("gcp"); err == nil || !strings.Contains(err.Error(), "NORI_SPRITES_API_TOKEN") {
			t.Fatalf("expected missing token error, got %v", err)
		}

		t.Setenv("NORI_SPRITES_API_TOKEN", "sp_live_super_secret")
		s, err := LoadDefaultCloud("gcp")
		if err != nil {
			t.Fatalf("LoadDefaultCloud with token: %v", err)
		}
		if !s.Computer.Enabled() || s.Computer.Provider != "sprites" {
			t.Fatalf("expected sprites computer enabled, got %+v", s.Computer)
		}
		if !slices.Contains(s.Observability.Credentials, "sp_live_super_secret") {
			t.Fatalf("expected NORI_SPRITES_API_TOKEN to be registered in Observability.Credentials for redaction")
		}
	})

	t.Run("docker rejected on gcp", func(t *testing.T) {
		clearModelEnv(t)
		t.Setenv("NORI_CLOUD", "gcp")
		t.Setenv("ARTIFACT_BUCKET", "nori-test-bucket")
		t.Setenv("NORI_COMPUTER_PROVIDER", "docker")

		if _, err := LoadDefaultCloud("gcp"); err == nil || !strings.Contains(err.Error(), "NORI_CLOUD=local") {
			t.Fatalf("expected local cloud error, got %v", err)
		}
	})
}
