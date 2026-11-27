package parse

import (
	"errors"
	"fmt"

	"github.com/ashtonian/pkg/parse/core"
)

// Code identifies a parse error without an application error catalog.
type Code = core.Code

// ParseError carries a code and the location and context of a refused request.
type ParseError = core.ParseError

// The schema discovery codes parsehttp.SchemaHandler and
// parsehttp.SchemaListHandler answer.
const (
	// ECodeSchemaNotFound is a resource the registry has not registered,
	// answered 404.
	ECodeSchemaNotFound = core.CodeSchemaNotFound
	// ECodeSchemaEncodingFailed is a schema document that could not be
	// encoded: a server defect, answered 500.
	ECodeSchemaEncodingFailed = core.CodeSchemaEncodingFailed
)

// Codes returns every code parse and its packages answer, in order, each
// with one status and message. filter.Codes and patch.Codes are the codes of
// their grammars; a limit both enforce has one code.
func Codes() []Code { return core.Codes() }

// ErrNoSuchKey is ParseKey's refusal of a key's text: text a key column
// cannot hold, such as letters for an integer column, or text holding
// U+0000 or invalid UTF-8 (core.CheckText). The text is the request's, an
// item route's path segment, and no row has that key, so answer it as the
// row not found. ErrKeyInvalid, unlike it, is the server's.
var ErrNoSuchKey = errors.New("no key of the key columns has this text")

// ErrTenantInvalid is a Scope.Tenant a tenant column the request reaches
// cannot hold: text that is not a UUID for a UUID column, or not a decimal
// integer in range for an integer column. Like ErrTenantRequired, it is the
// server's mistake, never the request's.
var ErrTenantInvalid = errors.New("Scope.Tenant is not a value of the tenant column's type")

// ErrTenantRequired is a list of a tenant-scoped resource built with
// neither Scope.Tenant nor Scope.Governance, or an update without
// Scope.Tenant. It is the server's mistake, never the request's.
var ErrTenantRequired = errors.New("a tenant-scoped resource needs Scope.Tenant")

// ErrNoTarget is an update built without keys or a non-empty filter.
var ErrNoTarget = errors.New("an update needs a key or a filter")

// ErrKeyInvalid is a target key, a Target's or an update batch row's, the
// key columns cannot hold: a value of another Go type, text that is not a
// UUID for a UUID column, or another number of values than the key has
// columns (ByID on a key of two). The key is the server's, from its route
// or its store, so a client never sees it as a filter error.
var ErrKeyInvalid = errors.New("not a value of the key column's type")

// ErrForeign is a request value a resource did not parse handed to it: a
// ListRequest, filter or patch another resource parsed, a WithPaths view's
// or its base's included, or one assembled by hand. A resource compiles and
// applies only what its own schemas validated; parse the value with it.
var ErrForeign = errors.New("not parsed by this resource")

// ErrUpdateScope is an update built with a widening Scope: an update
// reaches only its own tenant's live rows, so Governance and IncludeDeleted
// do not apply to it.
var ErrUpdateScope = errors.New("an update reaches only its tenant's live rows")

// ErrUpdateBatchTargets means the locked preimages do not match every named key.
var ErrUpdateBatchTargets = errors.New("update batch targets did not match")

// ErrCommitUnknown is an update whose COMMIT was sent and never answered:
// the connection failed, or the settle timeout ran out, while the database
// was committing. The update may have been applied. Every other error from
// Update or UpdateBatch means nothing the update wrote is kept; after this
// one, read the rows back before retrying a write that is not idempotent.
// errors.Is matches it; the commit's own error is under it.
var ErrCommitUnknown = errors.New("commit outcome unknown")

// ErrProviderMismatch is a resource given a transaction of another
// provider (In), or a relation target built on another provider (Relate):
// a transaction holds one database's statements, and a relation joins
// tables of one database.
var ErrProviderMismatch = errors.New("not the resource's provider")

// ErrRegistered is a registration under a name the registry already holds.
var ErrRegistered = errors.New("already registered")

// StatementError is the failure of one statement a resource ran on its
// provider, as the database or the scan of its rows reported it. Statement
// names it: a list's page, count, facets or include <field>; a query; an
// update's current, conflict, lock, owner, relations or reread; or a step
// of the transaction an update runs in: begin, savepoint, release
// savepoint, rollback to savepoint, rollback or commit. Every provider
// names its statements so, for a log attribute or a metric label.
type StatementError struct {
	// Table is the table of the resource whose statement it is; empty for
	// a transaction's own begin, commit or rollback (Provider.Begin, Tx).
	Table     string
	Statement string
	Err       error
}

func (e *StatementError) Error() string {
	if e.Table == "" {
		return e.Statement + ": " + e.Err.Error()
	}
	return "parse: " + e.Table + ": " + e.Statement + ": " + e.Err.Error()
}

func (e *StatementError) Unwrap() error { return e.Err }

// StatementFailed is err as the failure of statement, one of table's
// resource ("" for a transaction's own), a *StatementError, or nil when err
// is nil, for parse's provider packages, which report each statement's
// failure so.
func StatementFailed(table, statement string, err error) error {
	if err == nil {
		return nil
	}
	failed := &StatementError{Table: table, Statement: statement, Err: err}
	return failed
}

// BodyReadError is a request body that could not be read to its end,
// usually a client that went away or sent less than it declared. It is no
// *core.ParseError, since no body arrived to be judged, and no defect of the
// server: a handler answers it as a bad request, or not at all when the
// client is gone. Err is the reader's error.
type BodyReadError struct {
	// Table is the resource's table.
	Table string
	// Request is the body's request: "list" or "patch".
	Request string
	Err     error
}

func (e *BodyReadError) Error() string {
	return fmt.Sprintf("parse: %s: reading %s body: %v", e.Table, e.Request, e.Err)
}

func (e *BodyReadError) Unwrap() error { return e.Err }
