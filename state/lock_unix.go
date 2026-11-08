//go:build linux || darwin

package state

import (
	"fmt"
	"os"
	"syscall"
)

func lockController(file *os.File) error {
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("local controller already active or lock unavailable: %w", err)
	}
	return nil
}
