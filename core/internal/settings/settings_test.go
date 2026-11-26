package settings

import (
	"strings"
	"testing"
)

func TestLoadGCPCloudValidation(t *testing.T) {
	t.Run("accepts gcp with ARTIFACT_BUCKET and without AWS settings", func(t *testing.T) {
		clearModelEnv(t)
		t.Setenv("NORI_CLOUD", "gcp")
		t.Setenv("ARTIFACT_BUCKET", "nori-prod-artifacts")
		t.Setenv("AWS_REGION", "")
		t.Setenv("AWS_DEFAULT_REGION", "")
		t.Setenv("NORI_CREDENTIAL_KEY_SECRET", "")

		s, err := LoadDefaultCloud("gcp")
		if err != nil {
			t.Fatalf("LoadDefaultCloud(gcp): %v", err)
		}
		if s.Cloud != "gcp" {
			t.Fatalf("Cloud = %q, want gcp", s.Cloud)
		}
		if s.EnvProfile != "prod" {
			t.Fatalf("EnvProfile = %q, want prod", s.EnvProfile)
		}
		if s.Store.ArtifactBucket != "nori-prod-artifacts" {
			t.Fatalf("ArtifactBucket = %q, want nori-prod-artifacts", s.Store.ArtifactBucket)
		}
	})

	t.Run("requires ARTIFACT_BUCKET on gcp", func(t *testing.T) {
		clearModelEnv(t)
		t.Setenv("NORI_CLOUD", "gcp")
		t.Setenv("ARTIFACT_BUCKET", "")
		t.Setenv("NORI_CREDENTIAL_KEY_SECRET", "")

		_, err := LoadDefaultCloud("gcp")
		if err == nil || !strings.Contains(err.Error(), "ARTIFACT_BUCKET must be configured") {
			t.Fatalf("expected ARTIFACT_BUCKET missing error, got %v", err)
		}
	})

	t.Run("rejects non-prod profile on gcp", func(t *testing.T) {
		clearModelEnv(t)
		t.Setenv("NORI_CLOUD", "gcp")
		t.Setenv("NORI_ENV_PROFILE", "dev")
		t.Setenv("ARTIFACT_BUCKET", "nori-prod-artifacts")
		t.Setenv("NORI_CREDENTIAL_KEY_SECRET", "")

		_, err := LoadDefaultCloud("gcp")
		if err == nil || !strings.Contains(err.Error(), "NORI_ENV_PROFILE must be prod when NORI_CLOUD=gcp") {
			t.Fatalf("expected prod profile error, got %v", err)
		}
	})

	t.Run("fails fast if NORI_CREDENTIAL_KEY_SECRET is set without AWS", func(t *testing.T) {
		for _, cloud := range []string{"gcp", "local"} {
			t.Run(cloud, func(t *testing.T) {
				clearModelEnv(t)
				t.Setenv("NORI_CLOUD", cloud)
				if cloud == "gcp" {
					t.Setenv("ARTIFACT_BUCKET", "nori-prod-artifacts")
				}
				t.Setenv("NORI_CREDENTIAL_KEY_SECRET", "arn:aws:secretsmanager:us-east-1:123:secret:nori")

				_, err := LoadDefaultCloud(cloud)
				if err == nil || !strings.Contains(err.Error(), "NORI_CREDENTIAL_KEY_SECRET requires NORI_CLOUD=aws") {
					t.Fatalf("expected NORI_CREDENTIAL_KEY_SECRET error for %s, got %v", cloud, err)
				}
			})
		}
	})

	t.Run("rejects unknown cloud values", func(t *testing.T) {
		clearModelEnv(t)
		t.Setenv("NORI_CLOUD", "azure")
		t.Setenv("NORI_CREDENTIAL_KEY_SECRET", "")

		_, err := LoadDefaultCloud("aws")
		if err == nil || !strings.Contains(err.Error(), "NORI_CLOUD must be aws, gcp, or local") {
			t.Fatalf("expected invalid cloud error, got %v", err)
		}
	})
}
