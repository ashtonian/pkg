// Package state owns bounded, durable local operator state.
package state

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ReadJSON reads one bounded state document. Missing files retain os.ErrNotExist.
func ReadJSON(ctx context.Context, file string, maxBytes int64, target any) error {
	return readJSON(ctx, file, maxBytes, target, false)
}

// ReadJSONStrict also rejects fields outside the owning versioned schema.
func ReadJSONStrict(ctx context.Context, file string, maxBytes int64, target any) error {
	return readJSON(ctx, file, maxBytes, target, true)
}
func readJSON(ctx context.Context, file string, maxBytes int64, target any, strict bool) error {
	data, err := ReadFile(ctx, file, maxBytes)
	if err != nil {
		return err
	}
	return decodeDocument(ctx, data, target, strict)
}

func decodeDocument(ctx context.Context, data []byte, target any, strict bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strict {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(target); err != nil {
			return fmt.Errorf("decode state: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return errors.New("state must contain exactly one JSON document")
		}
	} else if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode state: %w", err)
	}
	return nil
}

// WriteJSON replaces a state document atomically with owner-only permissions and fsync.
// A directory-sync failure after rename means the new value exists but durability is uncertain.
func WriteJSON(ctx context.Context, file string, value any) error {
	return writeJSON(ctx, file, value, 0)
}

// WriteJSONBounded rejects an unreadable-size replacement before creating files or directories.
func WriteJSONBounded(ctx context.Context, file string, maxBytes int64, value any) error {
	if maxBytes <= 0 || maxBytes > 128<<20 {
		return errors.New("state write bound must be between 1 byte and 128 MiB")
	}
	return writeJSON(ctx, file, value, maxBytes)
}
func writeJSON(ctx context.Context, file string, value any, maxBytes int64) (err error) {
	return commitJSON(ctx, file, value, maxBytes, true)
}

// CreateJSONBounded atomically creates private state without replacing an existing run or symlink.
func CreateJSONBounded(ctx context.Context, file string, maxBytes int64, value any) error {
	if maxBytes <= 0 || maxBytes > 128<<20 {
		return errors.New("state write bound must be between 1 byte and 128 MiB")
	}
	return commitJSON(ctx, file, value, maxBytes, false)
}

func encodeJSON(ctx context.Context, value any, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		return nil, errors.New("state document exceeds write bound")
	}
	return data, ctx.Err()
}

func commitJSON(ctx context.Context, file string, value any, maxBytes int64, replace bool) (err error) {
	if file == "" || os.IsPathSeparator(file[len(file)-1]) || !stateName(filepath.Base(file)) {
		return errors.New("state destination requires a file name")
	}
	data, err := encodeJSON(ctx, value, maxBytes)
	if err != nil {
		return err
	}
	directory := filepath.Dir(file)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return fmt.Errorf("open state directory: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	return commitAt(ctx, root, filepath.Base(file), data, replace)
}

func stateName(name string) bool {
	return name != "" && name != "." && name != ".." && !filepath.IsAbs(name) && filepath.Base(name) == name
}

// ReadJSONStrictAt reads one regular, non-symlink state file beneath a caller-owned directory handle.
func ReadJSONStrictAt(ctx context.Context, root *os.Root, name string, maxBytes int64, target any) error {
	data, err := ReadFileAt(ctx, root, name, maxBytes)
	if err != nil {
		return err
	}
	return decodeDocument(ctx, data, target, true)
}

// WriteJSONAt atomically replaces private bounded state within a caller-owned directory handle.
func WriteJSONAt(ctx context.Context, root *os.Root, name string, maxBytes int64, value any) error {
	return writeAt(ctx, root, name, maxBytes, value, true)
}

// WriteBytesAt atomically replaces one bounded private file without reformatting its content.
func WriteBytesAt(ctx context.Context, root *os.Root, name string, maxBytes int64, data []byte) error {
	if root == nil || !stateName(name) || maxBytes <= 0 || maxBytes > 128<<20 || int64(len(data)) > maxBytes {
		return errors.New("rooted byte write requires a directory, leaf name and bounded content")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return commitAt(ctx, root, name, bytes.Clone(data), true)
}

// CreateJSONAt atomically creates private bounded state without replacing any existing directory entry.
func CreateJSONAt(ctx context.Context, root *os.Root, name string, maxBytes int64, value any) error {
	return writeAt(ctx, root, name, maxBytes, value, false)
}

func writeAt(ctx context.Context, root *os.Root, name string, maxBytes int64, value any, replace bool) error {
	if root == nil || !stateName(name) || maxBytes <= 0 || maxBytes > 128<<20 {
		return errors.New("rooted state write requires a directory, leaf name and bounded size")
	}
	data, err := encodeJSON(ctx, value, maxBytes)
	if err != nil {
		return err
	}
	return commitAt(ctx, root, name, data, replace)
}

func commitAt(ctx context.Context, root *os.Root, name string, data []byte, replace bool) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	temporary := ".ops-state-" + rand.Text()
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create state temporary file: %w", err)
	}
	defer func() {
		if file != nil {
			err = errors.Join(err, file.Close())
		}
		if temporary != "" {
			if removeErr := root.Remove(temporary); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, removeErr)
			}
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync state: %w", err)
	}
	closeErr := file.Close()
	file = nil
	if closeErr != nil {
		return fmt.Errorf("close state: %w", closeErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if replace {
		if err := root.Rename(temporary, name); err != nil {
			return fmt.Errorf("replace state: %w", err)
		}
	} else {
		if err := root.Link(temporary, name); err != nil {
			return fmt.Errorf("create state: %w", err)
		}
		if err := root.Remove(temporary); err != nil {
			return fmt.Errorf("remove committed state temporary file: %w", err)
		}
	}
	temporary = ""
	return SyncRoot(ctx, root)
}

// CreateBytesAt commits bounded byte-exact output without replacing any existing entry.
func CreateBytesAt(ctx context.Context, root *os.Root, name string, maxBytes int64, data []byte) error {
	if root == nil || !stateName(name) || maxBytes < 1 || maxBytes > 128<<20 || int64(len(data)) > maxBytes {
		return errors.New("byte creation requires a rooted bounded payload")
	}
	return commitAt(ctx, root, name, bytes.Clone(data), false)
}
