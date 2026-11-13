package pg

import (
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// userTimeout sets TCP_USER_TIMEOUT on a connection's socket: unacknowledged
// data fails the connection after d.
func userTimeout(d time.Duration) func(network, address string, c syscall.RawConn) error {
	if d <= 0 {
		return nil
	}
	return func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_USER_TIMEOUT, int(d.Milliseconds()))
		}); err != nil {
			return err
		}
		return serr
	}
}
