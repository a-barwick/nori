package sim_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repoRootFromSim(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

func TestEdgeRunWithEnvSecretHygiene(t *testing.T) {
	root := repoRootFromSim(t)
	scriptPath := filepath.Join(root, "scripts", "run-with-env.mjs")

	missingPath := filepath.Join(t.TempDir(), "missing.env")
	missingCmd := exec.Command("node", scriptPath, missingPath, "node", "-e", "process.exit(0)")
	var missingOut bytes.Buffer
	missingCmd.Stdout = &missingOut
	missingCmd.Stderr = &missingOut
	if err := missingCmd.Run(); err == nil {
		t.Fatalf("expected non-zero exit for missing .env file")
	}
	msg := missingOut.String()
	if !strings.Contains(msg, ".env.example") {
		t.Fatalf("expected missing .env message to mention .env.example, got: %q", msg)
	}
	if strings.Contains(msg, "ENOENT") || strings.Contains(msg, "at readFileSync") {
		t.Fatalf("expected clean error without ENOENT stack trace, got: %q", msg)
	}

	strippedKeys := []string{
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
		"OPENROUTER_MANAGEMENT_KEY",
		"TEMPORAL_BILLING_API_KEY",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_PROD_API_KEY",
		"ANTHROPIC_DEV_API_KEY",
		"ANTHROPIC_TEST_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"OPENAI_API_KEY",
		"OPENAI_PROD_API_KEY",
		"OPENAI_DEV_API_KEY",
		"OPENAI_TEST_API_KEY",
		"OPENROUTER_PROD_API_KEY",
		"OPENROUTER_DEV_API_KEY",
		"OPENROUTER_TEST_API_KEY",
		"NORI_POSTGRES_URL",
		"NORI_POSTGRES_LISTEN_URL",
		"NORI_CREDENTIAL_KEY",
		"NORI_CREDENTIAL_KEY_SECRET",
		"NORI_CREDENTIAL_KEY_FILE",
		"TEMPORAL_API_KEY",
		"TEMPORAL_API_KEY_SECRET_VERSION",
		"OPENROUTER_API_KEY",
		"JEV_API_KEY",
		"BRAVE_SEARCH_API_KEY",
		"NORI_MODEL_GATEWAY_API_KEY",
		"NORI_USAGE_TOKEN",
		"NORI_SAFE_PUBLIC_FLAG",
	}

	var envLines []string
	for _, key := range strippedKeys {
		envLines = append(envLines, key+"=secret-value-"+key)
	}
	envLines = append(envLines,
		"NEXT_PUBLIC_SENTRY_ENVIRONMENT=staging",
		"NORI_APP_ORIGIN=https://nori.example.test",
	)
	envPath := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envPath, []byte(strings.Join(envLines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write temp env: %v", err)
	}

	probeCmd := exec.Command("node", scriptPath, envPath, "node", "-e", "process.stdout.write(JSON.stringify(process.env))")
	probeCmd.Env = append(os.Environ(),
		"AWS_SECRET_ACCESS_KEY=parent-aws-secret",
		"NORI_POSTGRES_URL=postgres://parent-secret",
	)
	var probeOut bytes.Buffer
	var probeErr bytes.Buffer
	probeCmd.Stdout = &probeOut
	probeCmd.Stderr = &probeErr
	if err := probeCmd.Run(); err != nil {
		t.Fatalf("run-with-env probe failed: %v\nstderr: %s", err, probeErr.String())
	}
	var childEnv map[string]string
	if err := json.Unmarshal(probeOut.Bytes(), &childEnv); err != nil {
		t.Fatalf("decode child env: %v", err)
	}
	for _, key := range strippedKeys {
		if val, ok := childEnv[key]; ok {
			t.Fatalf("expected unallowed key %s to be absent from child env, got %q", key, val)
		}
	}
	if childEnv["NEXT_PUBLIC_SENTRY_ENVIRONMENT"] != "staging" {
		t.Fatalf("expected allowlisted web var NEXT_PUBLIC_SENTRY_ENVIRONMENT to pass through, got %q", childEnv["NEXT_PUBLIC_SENTRY_ENVIRONMENT"])
	}
	if childEnv["NORI_APP_ORIGIN"] != "https://nori.example.test" {
		t.Fatalf("expected allowlisted web var NORI_APP_ORIGIN to pass through, got %q", childEnv["NORI_APP_ORIGIN"])
	}
}
