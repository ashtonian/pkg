//go:build linux || darwin

package state

import "syscall"

func nonblockingReadFlag() int { return syscall.O_NONBLOCK }
