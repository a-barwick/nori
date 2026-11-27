package docker_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"nori/internal/computer"
	"nori/internal/computer/docker"
)

func TestDockerProviderEnsureIsolationAndLifecycle(t *testing.T) {
	ctx := context.Background()
	var calls [][]string
	containerExists := false
	containerRunning := false

	p := docker.New(docker.Config{
		Image:         "nori-computer:test",
		LocalhostOnly: true,
		MemoryLimitMB: 2048,
		Runner: func(_ context.Context, _ []byte, args ...string) (string, string, int, error) {
			calls = append(calls, slices.Clone(args))
			switch args[0] {
			case "inspect":
				if !containerExists {
					return "", "No such object", 1, nil
				}
				if containerRunning {
					return "running\n", "", 0, nil
				}
				return "exited\n", "", 0, nil
			case "run":
				containerExists = true
				containerRunning = true
				return "cid123\n", "", 0, nil
			case "start":
				containerRunning = true
				return "", "", 0, nil
			case "stop":
				containerRunning = false
				return "", "", 0, nil
			case "port":
				return "127.0.0.1:49152\n", "", 0, nil
			case "exec":
				return "ok\n", "", 0, nil
			case "rm":
				containerExists = false
				containerRunning = false
				return "", "", 0, nil
			case "volume":
				return "", "", 0, nil
			default:
				return "", "", 0, nil
			}
		},
	})

	inst, err := p.Ensure(ctx, computer.Spec{
		UserID:        "u-123",
		ComputerID:    "cmp_abc",
		DisplayWidth:  1280,
		DisplayHeight: 800,
		MemoryLimitMB: 2048,
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if inst.ProviderID != "nori-computer-cmp-abc" {
		t.Fatalf("ProviderID = %q", inst.ProviderID)
	}

	// Verify docker run args enforce persistent volume, loopback port, metadata SSRF block, and localhost-only network.
	var runArgs []string
	for _, c := range calls {
		if len(c) > 0 && c[0] == "run" {
			runArgs = c
			break
		}
	}
	joined := strings.Join(runArgs, " ")
	for _, want := range []string{
		"--volume nori-home-cmp-abc:/home/nori",
		"--publish 127.0.0.1::8080",
		"--add-host metadata.google.internal:127.0.0.1",
		"--network none",
		"--memory 2048m",
		"--security-opt no-new-privileges:true",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("docker run missing %q in: %s", want, joined)
		}
	}

	// DesktopEndpoint resolves loopback websockify URL.
	target, err := p.DesktopEndpoint(ctx, inst.ProviderID)
	if err != nil {
		t.Fatalf("DesktopEndpoint: %v", err)
	}
	if target.WSURL != "ws://127.0.0.1:49152/websockify" {
		t.Fatalf("WSURL = %q", target.WSURL)
	}

	// Sleep stops container; Ensure starts it back up preserving persistent volume.
	if err := p.Sleep(ctx, inst.ProviderID); err != nil {
		t.Fatalf("Sleep: %v", err)
	}
	st, err := p.Status(ctx, inst.ProviderID)
	if err != nil || st.Status != computer.StatusSleeping {
		t.Fatalf("Status after sleep = (%+v, %v), want sleeping", st, err)
	}
	if _, err := p.Ensure(ctx, computer.Spec{UserID: "u-123", ComputerID: "cmp_abc"}); err != nil {
		t.Fatalf("Ensure wake: %v", err)
	}

	// Destroy removes container and named volume.
	if err := p.Destroy(ctx, inst.ProviderID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := p.Status(ctx, inst.ProviderID); err != computer.ErrNotFound {
		t.Fatalf("Status after destroy = %v, want ErrNotFound", err)
	}
}

func TestDockerProviderExecSetsNPMPrefixAndPathPrecedence(t *testing.T) {
	var gotArgs []string
	p := docker.New(docker.Config{
		Runner: func(_ context.Context, _ []byte, args ...string) (string, string, int, error) {
			gotArgs = slices.Clone(args)
			return "ok\n", "", 0, nil
		},
	})

	if _, err := p.Exec(context.Background(), "nori-computer-cmp-abc", computer.ExecRequest{
		Command: "which claude",
		WorkDir: computer.DefaultWorkDir,
	}); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{
		"bash -lc",
		`NPM_CONFIG_PREFIX="/home/nori/.local/npm"`,
		`PATH="/home/nori/.nori/bin:/home/nori/.local/npm/bin:/home/nori/.local/bin:/home/nori/.local/venv/bin:$PATH"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("docker exec missing %q in: %s", want, joined)
		}
	}
}
