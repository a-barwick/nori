package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"nori/internal/users"
)

func TestInviteCLISubcommands(t *testing.T) {
	dir := users.NewMemoryDirectory()
	open := func(context.Context) (users.Directory, error) { return dir, nil }
	ctx := context.Background()

	var createOut bytes.Buffer
	if err := runCLI(ctx, []string{"invite", "create", "-max-uses", "3", "-domain", "example.com", "-expires", "24h", "-note", "founders"}, open, &createOut); err != nil {
		t.Fatalf("invite create: %v", err)
	}
	code := strings.TrimSpace(createOut.String())
	if !strings.HasPrefix(code, "NORI-") {
		t.Fatalf("unexpected generated code: %q", code)
	}

	var listOut bytes.Buffer
	if err := runCLI(ctx, []string{"invite", "list"}, open, &listOut); err != nil {
		t.Fatalf("invite list: %v", err)
	}
	if !strings.Contains(listOut.String(), code) || !strings.Contains(listOut.String(), "0/3") || !strings.Contains(listOut.String(), "@example.com") {
		t.Fatalf("unexpected list output:\n%s", listOut.String())
	}

	var revokeOut bytes.Buffer
	if err := runCLI(ctx, []string{"invite", "revoke", code}, open, &revokeOut); err != nil {
		t.Fatalf("invite revoke: %v", err)
	}
	if !strings.Contains(revokeOut.String(), "revoked "+code) {
		t.Fatalf("unexpected revoke output: %q", revokeOut.String())
	}

	listOut.Reset()
	if err := runCLI(ctx, []string{"invite", "list"}, open, &listOut); err != nil {
		t.Fatalf("invite list after revoke: %v", err)
	}
	if !strings.Contains(listOut.String(), "revoked") {
		t.Fatalf("expected revoked status in list output:\n%s", listOut.String())
	}
}
