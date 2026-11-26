package llmguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
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
	}

	providerHosts := []string{
		"api.openai.com",
		"openrouter.ai",
		"api.anthropic.com",
		"api.typesafe.ai",
		"ai-gateway.vercel.sh",
		"generativelanguage.googleapis.com",
	}

	fset := token.NewFileSet()
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

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}

		var importsGuard bool
		httpAliases := map[string]bool{}
		for _, imp := range file.Imports {
			importPath, unqErr := strconv.Unquote(imp.Path.Value)
			if unqErr != nil {
				continue
			}
			if importPath == "nori/internal/llmguard" {
				importsGuard = true
			}
			if importPath == "net/http" {
				alias := "http"
				if imp.Name != nil && imp.Name.Name != "" && imp.Name.Name != "_" {
					alias = imp.Name.Name
				}
				httpAliases[alias] = true
			}
		}

		if approvedAdapters[rel] {
			if !importsGuard {
				t.Errorf("%s must import and route outbound HTTP requests through nori/internal/llmguard", rel)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.CompositeLit:
					if isHTTPClientType(node.Type, httpAliases) {
						pos := fset.Position(node.Pos())
						t.Errorf("%s:%d must not construct raw http.Client; use llmguard.Guard.Client / WrapClient", rel, pos.Line)
					}
				case *ast.CallExpr:
					if ident, ok := node.Fun.(*ast.Ident); ok && ident.Name == "new" && len(node.Args) == 1 && isHTTPClientType(node.Args[0], httpAliases) {
						pos := fset.Position(node.Pos())
						t.Errorf("%s:%d must not construct raw new(http.Client); use llmguard.Guard.Client / WrapClient", rel, pos.Line)
					}
				case *ast.SelectorExpr:
					if isPkgSelector(node, httpAliases, "DefaultClient") {
						pos := fset.Position(node.Pos())
						t.Errorf("%s:%d must not use http.DefaultClient; use llmguard.Guard.Client / WrapClient", rel, pos.Line)
					}
				}
				return true
			})
			return nil
		}

		if rel == "internal/modelkeys/service.go" {
			// Key validation reads provider metadata without running a model.
			var requestCalls int
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if isPkgSelector(sel, httpAliases, "NewRequest") {
					requestCalls++
					pos := fset.Position(call.Pos())
					t.Errorf("%s:%d must use http.NewRequestWithContext(ctx, http.MethodGet, ..., nil) for key validation", rel, pos.Line)
					return true
				}
				if isPkgSelector(sel, httpAliases, "NewRequestWithContext") {
					requestCalls++
					if len(call.Args) != 4 || !isMethodGet(call.Args[1], httpAliases) || !isNilIdent(call.Args[3]) {
						pos := fset.Position(call.Pos())
						t.Errorf("%s:%d may only issue a bodyless GET request for provider key validation", rel, pos.Line)
					}
				}
				return true
			})
			if requestCalls != 1 {
				t.Errorf("%s expected exactly 1 http.NewRequestWithContext call for key validation, found %d", rel, requestCalls)
			}
			return nil
		}

		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			val, unqErr := strconv.Unquote(lit.Value)
			if unqErr != nil {
				return true
			}
			for _, host := range providerHosts {
				if strings.Contains(val, host) {
					pos := fset.Position(lit.Pos())
					t.Errorf("%s:%d references LLM provider host %q outside approved llmguard-wrapped adapters", rel, pos.Line, host)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk core/: %v", err)
	}
}

func isPkgSelector(sel *ast.SelectorExpr, aliases map[string]bool, name string) bool {
	if sel == nil || sel.Sel == nil || sel.Sel.Name != name {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && aliases[ident.Name]
}

func isHTTPClientType(expr ast.Expr, aliases map[string]bool) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	return ok && isPkgSelector(sel, aliases, "Client")
}

func isMethodGet(expr ast.Expr, aliases map[string]bool) bool {
	if sel, ok := expr.(*ast.SelectorExpr); ok && isPkgSelector(sel, aliases, "MethodGet") {
		return true
	}
	if lit, ok := expr.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		val, err := strconv.Unquote(lit.Value)
		return err == nil && val == "GET"
	}
	return false
}

func isNilIdent(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "nil"
}
