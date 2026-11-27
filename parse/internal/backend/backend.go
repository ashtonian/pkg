// Package backend is the contract between parse's root package and the
// providers. The root package composes every request (filter.ComposedList
// for a list; an update's filter, keys, patch and options), scans every row
// into the resource's type and builds every page and result; it holds no
// executor. Each provider runs composed requests through an executor of its
// own (Executor), and hands every result's rows to a sink the root package
// implements (ListSink, UpdateSink, BatchSink, QuerySink). The SQL providers
// (parsesql, parsepgx) share one executor, internal/sqlexec, which compiles
// the requests to SQL through internal/sqlplan; nothing here assumes SQL, so
// a provider that evaluates requests in Go implements the same interfaces
// over its own store. The package imports filter, patch and core alone, and
// is internal so that only parse's own packages open a provider.
package backend

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// Provider is what a provider package hands the root package
// (parse.NewProvider): the provider's name in configuration, the dialect its
// SQL compiles for (nil for a provider that compiles none), its database,
// and the operations it supports.
type Provider struct {
	Name    string
	Dialect filter.Dialect
	// Storage is how the provider's database stores the values a row scan
	// reads back; nil is the Dialect's. A provider without a dialect whose
	// rows hold values as text sets it.
	Storage      Storage
	DB           DB
	Capabilities Capabilities
}

// Storage is how a provider's database stores column values, as a row scan
// reads them back: StoresAsText reports the types it holds as text the
// scan decodes, a timestamp's canonical text, a document's or an array's
// JSON (filter.Dialect is one). Name names it in a scan's refusals.
type Storage interface {
	Name() string
	StoresAsText(t core.FieldType) bool
}

// Capabilities is a set of the operations a provider may lack. The root
// package refuses an operation its provider lacks (parse.ErrUnsupported)
// before the request reaches the provider's executor, and never
// approximates it.
type Capabilities uint8

const (
	// SQLBuilders compiles a resource's requests to SQL for the caller:
	// parse.Resource's BuildList, BuildFacets, IncludeQueries, BuildUpdate,
	// BuildUpdateBatch, Page and PagePointers. It needs a Dialect.
	SQLBuilders Capabilities = 1 << iota
	// Query runs a statement of the application's own (parse.Resource.Query).
	Query
	// Search narrows a list or a facets request by full-text search.
	Search
	// ComputedColumns reads a column through an expression (parse.ReadExpr).
	ComputedColumns
	// StampSQL stamps an update with an expression the database evaluates
	// (parse.StampSQL).
	StampSQL
)

// AllCapabilities is every capability, as the SQL providers declare.
const AllCapabilities = SQLBuilders | Query | Search | ComputedColumns | StampSQL

// Has reports whether c holds every capability in want.
func (c Capabilities) Has(want Capabilities) bool { return c&want == want }

// String names c's capabilities, in declaration order: the name
// parse.UnsupportedError gives the one a provider lacks.
func (c Capabilities) String() string {
	names := []struct {
		capability Capabilities
		name       string
	}{
		{SQLBuilders, "SQL builders"},
		{Query, "Query"},
		{Search, "search"},
		{ComputedColumns, "computed columns"},
		{StampSQL, "StampSQL"},
	}
	var held []string
	for _, n := range names {
		if c.Has(n.capability) {
			held = append(held, n.name)
		}
	}
	if len(held) == 0 {
		return "no capability"
	}
	return strings.Join(held, ", ")
}

// Transaction is a transaction a provider package wraps for the root
// package (parse.NewTx): the name of the provider it belongs to and the
// transaction. A resource runs in it once its provider's database joins it
// (DB.Join).
type Transaction struct {
	Provider string
	Tx       Tx
}

// Rows are one result's rows as a provider reads them.
type Rows interface {
	filter.Rows
	// Documents names the result's columns the database reports as JSON
	// documents (PostgreSQL's json and jsonb), which a row scan decodes as
	// documents whatever the resource declares. Nil from a database that
	// reports no column types (SQLite).
	Documents() []string
}

// Resource is a resource's descriptor: what an executor knows of the table
// every request it runs reads. The root package builds it once, when the
// resource is built (parse.NewResource, and parse.Resource.WithPaths for a
// view), and nothing writes it after.
type Resource struct {
	Table string
	// Columns are the columns the resource reads, in scan order: every row
	// handed to a sink holds these, in this order.
	Columns []string
	// Key are the key columns, in key order, and KeyTypes their types with
	// the storage each declares.
	Key      []string
	KeyTypes []core.TypeInfo
	// TableColumns are every column of the resource's type, in field
	// order, each with its type and the storage it declares: the table a
	// provider that holds its own rows declares (Declarer). Columns are
	// among them.
	TableColumns []Column
	// Tenant and Deleted are the tenant and soft-delete columns, "" for
	// none.
	Tenant  string
	Deleted string
	// Schema is the filter schema: the fields, system fields and relations
	// the composed filters name.
	Schema *filter.ResourceSchema
	// Computed reports whether the resource reads a column through an
	// expression (parse.ReadExpr).
	Computed bool
	// SQL is the SQL text the resource's statements are compiled around;
	// nil on a provider without a dialect.
	SQL *SQLResource
}

// Column is one column of a resource's table and the type its values hold.
type Column struct {
	Name string
	Type core.TypeInfo
}

// Declarer is a provider's database that holds its tables itself rather
// than reading a schema the application created: the root package declares
// each resource's table to it as the resource is built (parse.NewResource)
// and again when a relation adds a pair table (parse.Resource.Relate).
// Declare declares res's table, with its key and every column of
// TableColumns, and the pair table of each many-to-many relation its
// schema holds, whose rows are keyed by every column. It is idempotent: a
// table already declared gains the columns it lacks. Resources on one
// table must agree on its key and on every column's type; a disagreement
// is refused and declares nothing.
type Declarer interface {
	Declare(res *Resource) error
}

// SQLResource is the SQL text a resource's descriptor carries for a
// provider with a dialect, rendered once for that dialect.
type SQLResource struct {
	// Projection selects Columns, each table-qualified and quoted, a
	// computed column through its expression, each under its own name.
	Projection string
}

// ListRequest is a composed list or facets request.
type ListRequest struct {
	Resource *Resource
	// List is the composed list: its filter with the floor and policy
	// merged in, keyset, cursor, limit, search, count and facets, and the
	// scope its relations are reached under.
	List *filter.ComposedList
	// Include names the relation fields whose related keys the page
	// carries.
	Include []string
}

// QueryRequest is a statement of the application's own over the resource's
// table, selecting its Columns (parse.Resource.Query).
type QueryRequest struct {
	Resource *Resource
	SQL      string
	Args     []any
}

// UpdateRequest is a composed update.
type UpdateRequest struct {
	Resource *Resource
	// Where selects the target rows: the floor a list in the same scope
	// reads (the tenant, live rows, the policy), ANDed with the target.
	Where *filter.FilterAST
	// Keys are the target's keys, each normalized to its key columns'
	// types, when the target is keys; nil when it is a filter.
	Keys []core.Key
	// Relations is the scope Where's relation predicates and the patch's
	// relation changes reach related rows under.
	Relations filter.RelationScope
	Patch     *patch.ParsedPatch
	Options   UpdateOptions
}

// UpdateOptions are what an update writes beyond its patch.
type UpdateOptions struct {
	// SkipUnchanged writes only the target rows the patch's columns change.
	SkipUnchanged bool
	// Stamp, when set, stamps a column on every update that writes one.
	Stamp *Stamp
	// Increments are integer columns moved one up on every update that
	// writes a column, in the order they were named.
	Increments []string
}

// Stamp is the column an update stamps and what it stamps it with: At, or
// SQL, an expression the database evaluates (the server's, trusted) when
// it is set.
type Stamp struct {
	Column string
	At     time.Time
	SQL    string
}

// UpdateBatchRequest is a composed update batch.
type UpdateBatchRequest struct {
	Resource *Resource
	// Rows are the batch's rows in key order, their keys distinct as the
	// provider's database holds them.
	Rows []BatchRow
	// Relations is the scope every row's Where reaches related rows under.
	Relations filter.RelationScope
	// MaxParameters bounds the parameters of each statement an executor
	// that binds parameters sends; zero is its own default.
	MaxParameters int
	Options       UpdateOptions
}

// BatchRow is one row of an update batch: its key, its column patch, and
// the filter selecting it, the batch's floor and policy and the row's own
// policy ANDed with its key.
type BatchRow struct {
	Key   core.Key
	Patch *patch.ParsedPatch
	Where *filter.FilterAST
}

// ListSink keeps a list's results and builds its page.
type ListSink interface {
	// Page scans the page's rows: up to the list's limit plus one, in the
	// keyset's order, or its reverse for a backward cursor.
	Page(Rows) error
	// Count keeps the size of the population the list matches without its
	// cursor, when it counts.
	Count(int64)
	// Facets keeps the population's facet counts, when the list asks for
	// them.
	Facets([]filter.FacetResult)
	// Keys builds the page from the rows Page scanned and returns its rows'
	// keys, normalized to the key columns' types, for the list's includes:
	// none when the list includes nothing or the page holds no row. An
	// executor that reads includes calls it once the page's rows are read.
	Keys() ([]core.Key, error)
	// Include keeps the related keys of one relation field the list
	// includes, by owner key.
	Include(field string, rows filter.IncludeRows)
}

// QuerySink keeps the rows of a statement of the application's own.
type QuerySink interface {
	Rows(Rows) error
}

// UpdateSink keeps an update's rows. Each scan replaces the rows the same
// method scanned before it.
type UpdateSink interface {
	// Targets scans every target row the update's filter selects as it
	// stands before the update writes, the rows it will leave among them:
	// for an executor that reads the targets apart from the rows it writes.
	Targets(Rows) error
	// Changed scans the rows the update wrote, as it left them.
	Changed(Rows) error
	// Unchanged scans target rows the update left as they were.
	Unchanged(Rows) error
	// Split ends the reads that decide the update's targets, before its
	// relation changes: the changed rows are Changed's, the unchanged ones
	// Unchanged's, then Targets' whose keys none of Changed's rows hold.
	Split() error
	// Keys are the keys of the changed and the unchanged rows, once Split,
	// normalized to the key columns' types: the owners of the update's
	// relation changes.
	Keys() (changed, unchanged []core.Key, err error)
	// RelationRows counts junction rows the relation changes touched.
	RelationRows(int64)
}

// BatchSink keeps an update batch's rows. An executor may run the batch in
// parts, such as statements bounded by their parameters.
type BatchSink interface {
	// Targets scans the target rows of one part, the rows with keys keys
	// the rows' filters select, as they stand before the batch writes. Each
	// call is the next part, numbered from zero.
	Targets(keys []core.Key, rows Rows) error
	// Check refuses the targets when a part's rows are not exactly its
	// keys, before the batch writes (parse.ErrUpdateBatchTargets).
	Check() error
	// Changed scans the rows the batch wrote of part, as it left them.
	Changed(part int, rows Rows) error
	// Split ends the batch's reads: each part's unchanged rows are its
	// targets whose keys none of its changed rows hold.
	Split() error
}

// Executor runs composed requests. A list's or a facets request's reads
// may run in whatever order and round trips the executor chooses, each in
// its own snapshot unless it runs in a transaction that holds one. An
// update or a batch runs in one transaction, or under a savepoint of the
// transaction the executor runs in, settled only once every row has
// scanned: an error means nothing it wrote is kept, unless
// errors.Is(err, parse.ErrCommitUnknown).
type Executor interface {
	List(ctx context.Context, req ListRequest, sink ListSink) error
	// Facets counts the rows the request matches by each facet it names;
	// nil when it names none.
	Facets(ctx context.Context, req ListRequest) ([]filter.FacetResult, error)
	Query(ctx context.Context, req QueryRequest, sink QuerySink) error
	Update(ctx context.Context, req UpdateRequest, sink UpdateSink) error
	UpdateBatch(ctx context.Context, req UpdateBatchRequest, sink BatchSink) error
}

// DB is a provider's database, which runs requests outside a transaction.
type DB interface {
	Executor
	// Begin begins a transaction the caller settles (parse.Provider.Begin).
	// The transaction is not bound to ctx's cancellation: it lasts until
	// Commit or Rollback.
	Begin(ctx context.Context) (Tx, error)
	// Join is the executor that runs requests in tx, a transaction Begin
	// began or a provider package wrapped (Transaction), configured as the
	// database is: a resource in a transaction (parse.Resource.In) runs as
	// its provider runs, whichever of the provider's transactions it is.
	Join(tx Tx) (Executor, error)
}

// Tx is a transaction a caller holds and settles. Rollback of a
// transaction already settled does nothing and returns nil.
type Tx interface {
	Executor
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// Compose is p composed for any executor under o's writes beyond the
// patch: the stamp and the increments, in the order they were named
// (patch.ComposePatch). An executor renders it (patch.ComposedPatch.SQL),
// or applies it to each target's stored values (patch.ComposedPatch.Apply).
func (o UpdateOptions) Compose(p *patch.ParsedPatch) (*patch.ComposedPatch, error) {
	var opts []patch.PatchSQLOption
	if o.Stamp != nil {
		if o.Stamp.SQL != "" {
			opts = append(opts, patch.WithAutoTimestampSQL(o.Stamp.Column, o.Stamp.SQL))
		} else {
			at := o.Stamp.At
			opts = append(opts, patch.WithAutoTimestamp(o.Stamp.Column), patch.WithTimeFunc(func() time.Time { return at }))
		}
	}
	for _, column := range o.Increments {
		opts = append(opts, patch.WithIncrement(column))
	}
	return patch.ComposePatch(p, opts...)
}

// CheckUpdate refuses an update that assigns a key column or the tenant
// column. NewResource refuses a patch path over either, so only a server
// column (parse.Stamp, parse.Increment) can name one here; every executor
// holds every update to the rule before it runs one, and an update that
// assigns nothing is never refused.
func (r *Resource) CheckUpdate(update *patch.ComposedPatch) error {
	return r.checkIdentity(update, "update")
}

// CheckBatchRow refuses a row of an update batch that the batch cannot
// write as one keyed row: one that changes relations, one that writes a
// member of a JSON document the stored row must decide (a batch locks and
// checks its rows by key alone; which operation a row's document fails is
// a single update's to report), and one that assigns an identity column
// (CheckUpdate).
func (r *Resource) CheckBatchRow(key core.Key, update *patch.ComposedPatch) error {
	if len(update.RelationOps()) != 0 {
		return fmt.Errorf("parse: %s: update batch relation mutations are unsupported", r.Table)
	}
	preconditions := update.Preconditions()
	if len(preconditions) != 0 {
		refused := core.NewError(patch.ECodeOpNotSQLCompilable, fmt.Sprintf("row %v: a batch update does not compile an operation below a JSON document field that the stored row must decide; update the row with BuildUpdate", key),
			core.WithPath(preconditions[0].Path))
		return refused
	}
	return r.checkIdentity(update, "update batch")
}

func (r *Resource) checkIdentity(update *patch.ComposedPatch, builder string) error {
	for _, column := range update.Columns() {
		if slices.Contains(r.Key, column) || (r.Tenant != "" && column == r.Tenant) {
			return fmt.Errorf("parse: %s: %s cannot change identity column %s", r.Table, builder, column)
		}
	}
	return nil
}
