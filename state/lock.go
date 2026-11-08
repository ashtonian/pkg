package state

import (
	"context"
	"errors"
	"os"
)

// LockFile holds nonblocking process-lifetime ownership until the returned file closes.
// Cooperating controllers must share the same trusted directory and leaf identity.
func LockFile(ctx context.Context, root *os.Root, name string) (file *os.File, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if root == nil || !stateName(name) {
		return nil, errors.New("controller lock requires a rooted leaf name")
	}
	if info, err := root.Lstat(name); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("controller lock must be a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err = root.OpenFile(name, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	defer func(owned *os.File) {
		if err != nil {
			err = errors.Join(err, owned.Close())
			file = nil
		}
	}(file)
	if err = file.Chmod(0o600); err != nil {
		return file, err
	}
	opened, err := file.Stat()
	if err != nil {
		return file, err
	}
	entry, err := root.Lstat(name)
	if err != nil {
		return file, err
	}
	if !opened.Mode().IsRegular() || !entry.Mode().IsRegular() || !os.SameFile(opened, entry) {
		return file, errors.New("controller lock identity changed")
	}
	if err = lockController(file); err != nil {
		return file, err
	}
	return file, nil
}
