//go:build linux

package computerd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// operationGroup manages descendants even when a command creates another session.
// This is process lifecycle management for the trusted computer, not a security
// boundary against another program running as the same Unix user.
type operationGroup struct {
	path string
	file *os.File
}

// delegatedRoot is the agent's delegated cgroup once prepareDelegation has moved the agent into a leaf below it;
// operation groups are created beside that leaf so controllers can be enabled for them.
var delegatedRoot string

func ownCgroup() (string, error) {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "0::/") {
			return filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(filepath.Clean(strings.TrimPrefix(line, "0::")), "/")), nil
		}
	}
	return "", errors.New("cgroup v2 delegation unavailable")
}

// prepareDelegation follows cgroup v2's no-internal-process rule: the agent moves itself into <own>/agent, then
// enables the memory, pids and cpu controllers on <own> so sibling operation groups can carry limits. Failure leaves
// the earlier layout, and sandboxing reports itself unavailable.
func prepareDelegation() error {
	own, err := ownCgroup()
	if err != nil {
		return err
	}
	if filepath.Base(own) == "agent" {
		own = filepath.Dir(own)
	} else {
		leaf := filepath.Join(own, "agent")
		if err := os.MkdirAll(leaf, 0755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(leaf, "cgroup.procs"), []byte("0"), 0600); err != nil {
			return fmt.Errorf("could not move the agent into its leaf cgroup: %w", err)
		}
	}
	if err := os.WriteFile(filepath.Join(own, "cgroup.subtree_control"), []byte("+memory +pids +cpu"), 0600); err != nil {
		return fmt.Errorf("could not enable cgroup controllers: %w", err)
	}
	delegatedRoot = own
	return nil
}

func newOperationGroup() (*operationGroup, error) {
	root := delegatedRoot
	if root == "" {
		own, err := ownCgroup()
		if err != nil {
			return nil, err
		}
		root = own
	}
	directory, err := os.MkdirTemp(root, "operation-")
	if err != nil {
		return nil, fmt.Errorf("delegated cgroup required: %w", err)
	}
	file, err := os.Open(directory)
	if err != nil {
		_ = os.Remove(directory)
		return nil, err
	}
	group := &operationGroup{path: directory, file: file}
	// Probe support and permissions without writing. Older kernels retain a
	// kill_seq after killing an empty cgroup and then SIGKILL the first child
	// launched into it with CLONE_INTO_CGROUP.
	probe, err := os.OpenFile(filepath.Join(directory, "cgroup.kill"), os.O_WRONLY, 0)
	if err != nil {
		group.close()
		return nil, fmt.Errorf("cgroup kill unavailable: %w", err)
	}
	if err = probe.Close(); err != nil {
		group.close()
		return nil, err
	}
	return group, nil
}
func (g *operationGroup) attributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(g.file.Fd())}
}
func (g *operationGroup) kill() error {
	return os.WriteFile(filepath.Join(g.path, "cgroup.kill"), []byte("1"), 0600)
}
func (g *operationGroup) stop(ctx context.Context) error {
	if err := g.kill(); err != nil {
		return err
	}
	for {
		data, err := os.ReadFile(filepath.Join(g.path, "cgroup.events"))
		if err != nil {
			return err
		}
		if strings.Contains("\n"+string(data), "\npopulated 0\n") {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}
func (g *operationGroup) close() { _ = g.file.Close(); _ = os.Remove(g.path) }

// newLimitedGroup creates an operation group with memory, process and CPU limits written before any process joins.
func newLimitedGroup(memoryMB, pids, cpuPercent int) (*operationGroup, error) {
	group, err := newOperationGroup()
	if err != nil {
		return nil, err
	}
	write := func(name, value string) error {
		return os.WriteFile(filepath.Join(group.path, name), []byte(value), 0600)
	}
	memoryMB = max(min(memoryMB, 4096), 16)
	pids = max(min(pids, 256), 1)
	cpuPercent = max(min(cpuPercent, 100), 5)
	if err = errors.Join(write("memory.max", fmt.Sprint(memoryMB<<20)), write("memory.swap.max", "0"),
		write("pids.max", fmt.Sprint(pids)), write("cpu.max", fmt.Sprintf("%d 100000", cpuPercent*1000))); err != nil {
		group.close()
		return nil, fmt.Errorf("cgroup controllers are not delegated: %w", err)
	}
	return group, nil
}

func (g *operationGroup) oomKilled() bool {
	data, err := os.ReadFile(filepath.Join(g.path, "memory.events"))
	return err == nil && !strings.Contains(string(data), "oom_kill 0")
}

// sandboxAttributes places the process in the limited group and in new user, network, mount and IPC namespaces: it
// keeps only loopback networking and its own user mapping.
func sandboxAttributes(g *operationGroup) *syscall.SysProcAttr {
	uid, gid := os.Getuid(), os.Getgid()
	return &syscall.SysProcAttr{
		UseCgroupFD: true, CgroupFD: int(g.file.Fd()), Setpgid: true,
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET | syscall.CLONE_NEWNS | syscall.CLONE_NEWIPC,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: uid, HostID: uid, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: gid, HostID: gid, Size: 1}},
		GidMappingsEnableSetgroups: false,
	}
}

func sandboxSupported() bool {
	if _, err := os.Stat("/proc/self/ns/user"); err != nil || delegatedRoot == "" {
		return false
	}
	group, err := newOperationGroup()
	if err != nil {
		return false
	}
	defer group.close()
	return true
}

func init() {
	if err := prepareDelegation(); err != nil {
		// Shell jobs keep working in the earlier layout; sandboxed packages are refused.
		fmt.Fprintln(os.Stderr, "computerd: cgroup controller delegation unavailable:", err)
	}
}
