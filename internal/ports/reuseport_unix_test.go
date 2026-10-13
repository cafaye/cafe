//go:build unix

package ports

import (
	"context"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// listenReusePort builds a listener with SO_REUSEPORT set — the trap this
// package's prober must not fall into.
//
// It exists only in test code, and it is here rather than in the test file
// because setting a socket option needs a syscall the standard library does not
// wrap, and because a helper a test uses must not become something a
// non-test caller can reach.
//
// # Why x/sys/unix and not syscall
//
// The standard library's frozen syscall package spells SO_REUSEPORT on the
// BSDs and darwin and does NOT spell it on Linux — `syscall.SO_REUSEPORT` is
// undefined at linux/amd64. So the first version of this file, which used
// syscall, compiled on the darwin machine most of this tree is written on and
// broke `bin/prime` on Linux CI with "undefined: syscall.SO_REUSEPORT". The
// maintained spelling of the same setsockopt is golang.org/x/sys/unix, which
// carries the constant for every platform this repository builds and tests for
// and is already in the module graph (golang.org/x/sys, indirect, via x/net).
//
// The other fix that compiles is to narrow the guard to `//go:build linux` and
// let reuseport_other_test.go answer on darwin — but darwin has SO_REUSEPORT to
// set, so that would trade a compile error for a silently skipped trap test on
// the developer's own machine. This keeps the assertion where it already
// worked and adds the platform that was missing.
func listenReusePort(network, address string) (net.Listener, error) {
	config := net.ListenConfig{
		Control: func(_, _ string, conn syscall.RawConn) error {
			var setErr error
			if err := conn.Control(func(fd uintptr) {
				setErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			}); err != nil {
				return err
			}
			return setErr
		},
	}
	return config.Listen(context.Background(), network, address)
}
