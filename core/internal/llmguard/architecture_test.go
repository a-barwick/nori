package llmguard

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoRawLLMCallsOutsideGuardedAdapters enforces at test time that:
//  1. No non-test Go source file outside the approved model adapter files talks
//     directly to LLM provider hosts (api.openai.com, openrouter.ai,
//     api.anthropic.com, api.typesafe.ai).
//  2. Every approved model adapter routes its HTTP client through llmguard
//     instead of constructing raw &http.Client{...} instances.
func TestNoRawLLMCallsOutsideGuardedAdapters(t *testing.T) {
	coreRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve core root: %v", err)
	}

	approvedAdapters := map[string]bool{
		filepath.ToSlash("internal/conversation/openai.go"):       true,
		filepath.ToSlash("internal/agent/responses/responses.go"): true,
		filepath.ToSlash("internal/conversation/jev.go"):          true,
	}

	providerHosts := []string{
		"api.openai.com",
		"openrouter.ai",
		"api.anthropic.com",
		"api.typesafe.ai",
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
