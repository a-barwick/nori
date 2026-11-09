package mcp

import (
	"context"
	"os/exec"
)

// localCommand starts a declared stdio server as a local process on the development stack. The argument vector
// comes from the deployment's declared commands, never from a request.
func localCommand(ctx context.Context, argv []string) *exec.Cmd {
	cmd := exec.CommandContext(context.WithoutCancel(ctx), argv[0], argv[1:]...)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin"}
	return cmd
}
