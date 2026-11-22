// Package rfc6902 names the members of an RFC 6902 operation object,
// which patch reads from a request and parse writes when it applies a
// patch itself.
package rfc6902

// The members of an operation object.
const (
	Op    = "op"
	Path  = "path"
	From  = "from"
	Value = "value"
)
