//go:build unit || integration || concurrency

// Package providertest opens parse providers for parse's tests: one that
// compiles statements for a dialect without a database, which every test
// lane builds, and providers on the fresh databases dbtest opens.
package providertest

import (
	"context"
	"errors"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
)

// ErrNoDatabase is a statement sent to a Compile provider.
var ErrNoDatabase = errors.New("providertest: a compile-only provider has no database")

// Compile is a provider for d with no database, for a test of the
// statements a resource builds for a database it does not open: builds
// compile for d, and running one fails with ErrNoDatabase. Its name is
// d's, as the provider package for d names its providers.
func Compile(d filter.Dialect) *parse.Provider {
	p := parse.NewProvider(backend.Provider{Name: d.Name(), Dialect: d, DB: noDatabase{}, SettleTimeout: parse.DefaultSettleTimeout})
	return p
}

type noDatabase struct{}

func (noDatabase) Send(context.Context, string, ...backend.Step) error { return ErrNoDatabase }

func (noDatabase) Transact(context.Context, string, time.Duration, func(backend.Conn) error) error {
	return ErrNoDatabase
}

func (noDatabase) Begin(context.Context) (backend.Tx, error) { return nil, ErrNoDatabase }
