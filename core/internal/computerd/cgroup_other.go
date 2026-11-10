//go:build !linux

package computerd

import (
	"context"
	"errors"
	"syscall"
)

type operationGroup struct{ path string }

func newOperationGroup() (*operationGroup, error) {
	return nil, errors.New("shell execution requires Linux cgroup v2 delegation")
}
func (*operationGroup) attributes() *syscall.SysProcAttr { return nil }
func (*operationGroup) kill() error                      { return errors.New("Linux cgroups unavailable") }
func (*operationGroup) stop(context.Context) error       { return errors.New("Linux cgroups unavailable") }
func (*operationGroup) close()                           {}

func newLimitedGroup(int, int, int) (*operationGroup, error) {
	return nil, errors.New("sandboxing requires Linux")
}
func (*operationGroup) oomKilled() bool                            { return false }
func sandboxAttributes(*operationGroup, bool) *syscall.SysProcAttr { return nil }
func sandboxSupported() bool                                       { return false }

// RunSandboxShim is Linux-only; elsewhere sandboxes with an allowlist are refused.
func RunSandboxShim([]string) int { return egressFailExit }
func egressSupported() bool       { return false }
