package state

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

// ReadFile follows managed symlinks to a bounded regular file and returns its exact bytes.
func ReadFile(ctx context.Context, path string, maximum int64) (data []byte, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" || maximum < 1 || maximum > 128<<20 {
		return nil, errors.New("file read requires a path and a bound through 128 MiB")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maximum {
		return nil, errors.New("input must be a bounded regular file")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|nonblockingReadFlag(), 0)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errors.New("input file identity changed while opening")
	}
	return readBytes(ctx, file, maximum)
}
func readBytes(ctx context.Context, input io.Reader, maximum int64) ([]byte, error) {
	var data []byte
	buffer := make([]byte, min(int64(64<<10), maximum+1))
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := input.Read(buffer[:min(int64(len(buffer)), maximum+1-int64(len(data)))])
		data = append(data, buffer[:count]...)
		if int64(len(data)) > maximum {
			return nil, errors.New("input exceeds its file byte bound")
		}
		if errors.Is(err, io.EOF) {
			return data, ctx.Err()
		}
		if err != nil {
			return nil, fmt.Errorf("read input: %w", err)
		}
	}
}
