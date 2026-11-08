//go:build !linux && !darwin

package state

import (
	"errors"
	"os"
)

func lockController(*os.File) error {
	return errors.New("local controller ownership requires Linux or macOS")
}
