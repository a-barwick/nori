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
