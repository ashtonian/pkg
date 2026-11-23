//go:build ruleguard

// Package gorules holds ruleguard rules that gocritic loads through .golangci.yml.
// It is a copy of lint/rules.go in the owner's private lint-baseline
// repository at 45fcf98f43fb: change the rules there, then copy the file again (CONTRIBUTING.md).
// The build tag keeps it out of normal builds; `go mod tidy` still sees it.
// golangci-lint's cache doesn't track this file: run `golangci-lint cache clean`
// after editing it.
package gorules

import "github.com/quasilyte/go-ruleguard/dsl"

// errorPrefix rejects error strings that state the failure instead of the operation.
// Wrapped, they stutter: "failed to load config: failed to open file: ...".
func errorPrefix(m dsl.Matcher) {
	m.Match(`fmt.Errorf($msg, $*_)`, `errors.New($msg)`).
		Where(m["msg"].Text.Matches(`(?i)^"(failed|unable|could not|cannot|error)\b`)).
		Report(`error strings name the operation, not the failure ("loading config: ...")`)
}

// ifInit rejects init statements in if headers. A statement whose direct parent is an
// IfStmt can only be its init: bodies and else branches are blocks. This catches every
// if and else-if, however long the chain.
func ifInit(m dsl.Matcher) {
	m.Match(`$*_ := $*_`, `$*_ = $*_`, `$_++`, `$_--`).
		Where(m["$$"].Node.Parent().Is("IfStmt")).
		Report(`declare on its own line before the if, not in the if header`)
}

// returnLiteral rejects struct, slice and map literals that have elements and are a
// return value, in any position, with or without &. Zero values (T{}) pass.
func returnLiteral(m dsl.Matcher) {
	m.Match(`$t{$_, $*_}`, `&$t{$_, $*_}`).
		Where(m["$$"].Node.Parent().Is("ReturnStmt")).
		Report(`assign the literal to a variable, then return the variable`)
}
