package cursor

import "errors"

var (
	ErrInvalidCursor    = errors.New("invalid cursor")
	ErrCursorGeneration = errors.New("cursor generation mismatch")
)
