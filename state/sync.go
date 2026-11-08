package state

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// SyncRoot durably records directory-entry changes; the caller retains the directory handle.
func SyncRoot(ctx context.Context, root *os.Root) (err error) {
	if root == nil {
		return errors.New("directory sync requires a root handle")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open state directory: %w", err)
	}
	defer func() { err = errors.Join(err, directory.Close()) }()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync state directory: %w", err)
	}
	return ctx.Err()
}
