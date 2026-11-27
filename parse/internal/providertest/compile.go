//go:build unit || integration || concurrency

// Package providertest is what every provider's tests share without a
// database: a provider that compiles statements for a dialect and runs none
// (Compile), one that compiles none and supports nothing a provider may
// lack (Bare), which every test lane builds, and a provider Config as its
// default tags describe it (Defaults).
package providertest

import (
	"context"
	"errors"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
	"github.com/ashtonian/pkg/parse/internal/sqlexec"
)

// ErrNoDatabase is a statement sent to a Compile provider, or a request
// handed to a Bare one.
var ErrNoDatabase = errors.New("providertest: a compile-only provider has no database")

// Compile is a provider for d with no database, for a test of the
// statements a resource builds for a database it does not open: builds
// compile for d, and running one compiles it as the SQL executor does and
// fails with ErrNoDatabase where a statement would be sent. Its name is
// d's, as the provider package for d names its providers, and a wrapped
// transaction of that name, with none under it, runs as its database
// does.
func Compile(d filter.Dialect) *parse.Provider {
	p := sqlexec.Provider(d.Name(), d, noDatabase{}, parse.DefaultSettleTimeout)
	p.DB = compiling{DB: p.DB}
	return parse.NewProvider(p)
}

// compiling is a Compile provider's database, which joins any transaction
// as itself: there is none to run in.
type compiling struct {
	backend.DB
}

func (c compiling) Join(backend.Tx) (backend.Executor, error) { return c.DB, nil }

type noDatabase struct{}

func (noDatabase) Send(context.Context, string, ...sqlexec.Step) error { return ErrNoDatabase }

func (noDatabase) Transact(context.Context, string, time.Duration, func(sqlexec.Conn) error) error {
	return ErrNoDatabase
}

func (noDatabase) Begin(context.Context) (sqlexec.Tx, error) { return nil, ErrNoDatabase }

// Bare is a provider named name that compiles no SQL (a nil dialect) and
// supports none of the operations a provider may lack
// (backend.Capabilities), with no database: a test of what the root
// package refuses before a request reaches a provider's executor
// (parse.ErrUnsupported). A request it does hand over fails with
// ErrNoDatabase.
func Bare(name string) *parse.Provider {
	p := parse.NewProvider(backend.Provider{Name: name, DB: bareDatabase{}})
	return p
}

// bareDatabase is a Bare provider's database: every request fails with
// ErrNoDatabase, and it joins any transaction as itself.
type bareDatabase struct{}

func (bareDatabase) List(context.Context, backend.ListRequest, backend.ListSink) error {
	return ErrNoDatabase
}

func (bareDatabase) Facets(context.Context, backend.ListRequest) ([]filter.FacetResult, error) {
	return nil, ErrNoDatabase
}

func (bareDatabase) Query(context.Context, backend.QueryRequest, backend.QuerySink) error {
	return ErrNoDatabase
}

func (bareDatabase) Update(context.Context, backend.UpdateRequest, backend.UpdateSink) error {
	return ErrNoDatabase
}

func (bareDatabase) UpdateBatch(context.Context, backend.UpdateBatchRequest, backend.BatchSink) error {
	return ErrNoDatabase
}

func (bareDatabase) Begin(context.Context) (backend.Tx, error) { return nil, ErrNoDatabase }

func (b bareDatabase) Join(backend.Tx) (backend.Executor, error) { return b, nil }
