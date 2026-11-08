//go:build !linux && !darwin

package process

import "os/exec"

func superviseGroup(*exec.Cmd) error { return ErrUnsupportedPlatform }
func killGroup(int) error            { return ErrUnsupportedPlatform }
