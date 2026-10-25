package cursor

import "errors"

var (
	ErrInvalidCursor        = errors.New("invalid cursor")
	ErrCursorGeneration     = errors.New("cursor generation mismatch")
	ErrCursorExpired        = errors.New("cursor has expired")
	ErrCursorTenantMismatch = errors.New("cursor tenant mismatch")
)
