//go:build !linux

package pg

import (
	"syscall"
	"time"
)

// userTimeout is Linux's TCP_USER_TIMEOUT; elsewhere (a developer's machine)
// the keepalives alone notice a dead peer.
func userTimeout(time.Duration) func(network, address string, c syscall.RawConn) error { return nil }
