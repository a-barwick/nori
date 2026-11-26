package llmguard

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoRawLLMCallsOutsideGuardedAdapters(t *testing.T) {
	coreRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve core root: %v", err)
	}

	approvedAdapters := map[string]bool{
		filepath.ToSlash("internal/conversation/openai.go"):       true,
		filepath.ToSlash("internal/conversation/anthropic.go"):    true,
		filepath.ToSlash("internal/agent/responses/responses.go"): true,
		filepath.ToSlash("internal/agent/responses/google.go"):    true,
		filepath.ToSlash("internal/platform/models.go"):           true,
		filepath.ToSlash("internal/agent/anthropic/anthropic.go"): true,
		filepath.ToSlash("internal/conversation/jev.go"):          true,
		filepath.ToSlash("internal/gateway/server.go"):            true,
	}

	providerHosts := []string{
		"api.openai.com",
		"openrouter.ai",
		"api.anthropic.com",
		"api.typesafe.ai",
		"ai-gateway.vercel.sh",
		"generativelanguage.googleapis.com",
	}

	err = filepath.WalkDir(coreRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(coreRoot, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(raw)

		if approvedAdapters[rel] {
			if !strings.Contains(src, `"nori/internal/llmguard"`) {
				t.Errorf("%s must import and route outbound HTTP requests through nori/internal/llmguard", rel)
			}
			if strings.Contains(src, "&http.Client{") {
				t.Errorf("%s must not construct raw &http.Client{}; use llmguard.Guard.Client / WrapClient", rel)
			}
			return nil
		}

		if rel == "internal/modelkeys/service.go" {
			// Key validation reads provider metadata without running a model.
			if strings.Count(src, "http.NewRequest") != 1 ||
				!strings.Contains(src, "http.NewRequestWithContext(valCtx, http.MethodGet, endpoint, nil)") {
				t.Errorf("%s may issue a single GET for provider key validation", rel)
			}
			return nil
		}

		for _, host := range providerHosts {
			if strings.Contains(src, host) {
				t.Errorf("%s references LLM provider host %q outside approved llmguard-wrapped adapters", rel, host)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk core/: %v", err)
	}
}
