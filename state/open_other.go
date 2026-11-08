//go:build !linux && !darwin

package state

func nonblockingReadFlag() int { return 0 }
