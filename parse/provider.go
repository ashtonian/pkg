package parse

import (
	"context"
	"fmt"
	"time"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
)

// Provider is the database an application's resources run on, opened by
// one of parse's provider packages: SQLite by parsesql.New, PostgreSQL by
// parsepgx.New, or whichever a setting names by parseprovider.Open. An
// application opens one at startup and builds every resource on it
// (NewResource); a resource composes its requests and hands them to the
// provider's executor, which runs them on its database for the resource's
// whole life. Providers are alternatives with one API: parse never moves
// data, cursors or plans from one provider to another, and never layers
// one on another. An operation a provider does not support is refused
// before anything runs (ErrUnsupported). A Provider is safe for concurrent
// use.
type Provider struct {
	name    string
	dialect filter.Dialect
	db      backend.DB
	// capabilities are the operations the provider supports; the others
	// are refused (supports).
	capabilities backend.Capabilities
}

// NewProvider is for parse's provider packages, which open a provider for
// an application (parsesql.New, parsepgx.New).
func NewProvider(p backend.Provider) *Provider {
	provider := &Provider{name: p.Name, dialect: p.Dialect, db: p.DB, capabilities: p.Capabilities}
	return provider
}

// supports refuses capability, one the provider lacks, with an
// *UnsupportedError for table's resource.
func (p *Provider) supports(table string, capability backend.Capabilities) error {
	if p.capabilities.Has(capability) {
		return nil
	}
	unsupported := &UnsupportedError{Provider: p.name, Capability: capability.String()}
	return fmt.Errorf("parse: %s: %w", table, unsupported)
}

// Name is the provider's name in configuration: sqlite or postgres.
func (p *Provider) Name() string { return p.name }

// Begin begins a transaction on the provider's database for a handler that
// runs several operations as one, each on a resource scoped to it
// (Resource.In). The transaction is not bound to ctx's cancellation: it
// lasts until Commit or Rollback, and a Rollback deferred past a Commit
// does nothing.
func (p *Provider) Begin(ctx context.Context) (*Tx, error) {
	tx, err := p.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("parse: %s: %w", p.name, err)
	}
	begun := &Tx{provider: p.name, began: p, tx: tx}
	return begun, nil
}

// Tx is a transaction resources run in (Resource.In): one Provider.Begin
// began, or an application's own that a provider package wraps
// (parsesql.Tx, parsepgx.Tx), so that the application's SQL and parse's
// statements commit together. An update in it runs under a savepoint, so a
// failed update leaves the transaction as it was and usable. A Tx is used
// by one goroutine at a time, as the transaction under it is.
type Tx struct {
	provider string
	// began is the provider that began the transaction; nil for one a
	// provider package wrapped, whose database the driver cannot name.
	began *Provider
	tx    backend.Tx
}

// NewTx is for parse's provider packages, which wrap an application's
// transaction (parsesql.Tx, parsepgx.Tx).
func NewTx(t backend.Transaction) *Tx {
	tx := &Tx{provider: t.Provider, tx: t.Tx}
	return tx
}

// Commit commits the transaction.
func (tx *Tx) Commit(ctx context.Context) error {
	err := tx.tx.Commit(ctx)
	if err != nil {
		return fmt.Errorf("parse: %s: %w", tx.provider, err)
	}
	return nil
}

// Rollback rolls the transaction back. A transaction already committed or
// rolled back is left as it is, and Rollback returns nil, so it can be
// deferred as soon as the transaction begins.
func (tx *Tx) Rollback(ctx context.Context) error {
	err := tx.tx.Rollback(ctx)
	if err != nil {
		return fmt.Errorf("parse: %s: %w", tx.provider, err)
	}
	return nil
}

// DefaultSettleTimeout bounds each statement that settles an update's
// transaction when a SQL provider is opened without a settle timeout.
//
// A SQL provider settles an update's transaction — its COMMIT or ROLLBACK,
// or in a transaction the resource is scoped to (In) the release of its
// savepoint or the rollback to it — once every statement has run and every
// row has scanned. The request may be gone by then, and a settle the
// request's cancellation cut short would leave the update half reported, so
// each settling statement runs under the request's values without its
// cancellation, bounded by a timeout of its own: a stalled database holds
// the call for that long and no longer. Each provider package takes the
// timeout as an option (parsesql.SettleTimeout, parsepgx.SettleTimeout).
const DefaultSettleTimeout = 10 * time.Second
