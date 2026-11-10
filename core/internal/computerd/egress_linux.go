//go:build linux

package computerd

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"

	"golang.org/x/sys/unix"
)

// RunSandboxShim is the first process inside a sandbox's network namespace when the sandbox has an egress
// allowlist. It brings loopback up, forwards 127.0.0.1:egressPort to the proxy's unix socket, then runs the program
// with proxy settings and exits with its code. If the forwarder cannot be established the program never runs.
// args: <socket path> -- <program> [args…]
func RunSandboxShim(args []string) int {
	if len(args) < 3 || args[1] != "--" {
		fmt.Fprintln(os.Stderr, egressFailMarker, "bad shim arguments")
		return egressFailExit
	}
	socket, program := args[0], args[2:]
	if err := loopbackUp(); err != nil {
		fmt.Fprintln(os.Stderr, egressFailMarker, "loopback:", err)
		return egressFailExit
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(egressPort))
	if err != nil {
		fmt.Fprintln(os.Stderr, egressFailMarker, "listen:", err)
		return egressFailExit
	}
	// The forwarder must reach the proxy before the program starts.
	probe, err := net.Dial("unix", socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, egressFailMarker, "proxy:", err)
		return egressFailExit
	}
	probe.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				upstream, err := net.Dial("unix", socket)
				if err != nil {
					return
				}
				defer upstream.Close()
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(upstream, conn); done <- struct{}{} }()
				go func() { _, _ = io.Copy(conn, upstream); done <- struct{}{} }()
				<-done
			}()
		}
	}()
	if err := dropPrivileges(); err != nil {
		fmt.Fprintln(os.Stderr, egressFailMarker, "privileges:", err)
		return egressFailExit
	}
	proxy := "http://127.0.0.1:" + strconv.Itoa(egressPort)
	cmd := exec.Command(program[0], program[1:]...)
	cmd.Env = append(os.Environ(), "HTTPS_PROXY="+proxy, "HTTP_PROXY="+proxy, "https_proxy="+proxy, "http_proxy="+proxy, "NO_PROXY=", "no_proxy=")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = cmd.Run()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		return exit.ExitCode()
	case err != nil:
		fmt.Fprintln(os.Stderr, "the program could not start:", err)
		return 127
	}
	return 0
}

// loopbackUp sets IFF_UP on lo; inside a fresh user and network namespace the shim holds CAP_NET_ADMIN for it.
func loopbackUp() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	ifr, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return err
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP)
	return unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr)
}

// dropPrivileges makes the program run with no capabilities even though the shim is root inside the namespace:
// root gains nothing on exec (securebits, locked), the bounding set is emptied and no exec can add privileges.
func dropPrivileges() error {
	// linux/securebits.h: NOROOT, NOROOT_LOCKED, NO_SETUID_FIXUP, NO_SETUID_FIXUP_LOCKED, KEEP_CAPS_LOCKED.
	const bits = 1<<0 | 1<<1 | 1<<2 | 1<<3 | 1<<5
	if err := unix.Prctl(unix.PR_SET_SECUREBITS, uintptr(bits), 0, 0, 0); err != nil {
		return fmt.Errorf("securebits: %w", err)
	}
	for c := 0; c <= unix.CAP_LAST_CAP; c++ {
		if err := unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(c), 0, 0, 0); err != nil && err != unix.EINVAL {
			return fmt.Errorf("bounding set: %w", err)
		}
	}
	return unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0)
}

func egressSupported() bool { return sandboxSupported() }
