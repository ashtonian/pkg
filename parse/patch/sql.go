package patch

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/dialect"
)

// PatchSQLResult holds the generated SQL SET clause and arguments.
type PatchSQLResult struct {
	// SetClause is the comma-separated `"col" = $N` fragment ready to drop
	// into UPDATE … SET, each column quoted by the dialect. Empty when the
	// patch only touches relation fields (junction-only patches).
	SetClause string

	// Assignments are the same SET expressions before comma-separated rendering.
	// Expressions share Args and its placeholder numbering.
	Assignments []ColumnAssignment

	// Args are the bound values for SetClause placeholders, in $N order.
	Args []any

	// ParamCount is the number of placeholders SetClause and Precondition
	// consume. Callers append filter params starting at startParam +
	// ParamCount.
	ParamCount int

	// Precondition, set when the patch writes a member of a JSON document
	// (an add, replace or remove below a field), is true for a row whose
	// stored documents hold what RFC 6902 needs to apply those operations:
	// each member a replace or a remove names, and each object an add sets
	// a member of, where the patch's earlier operations do not settle it.
	// The UPDATE must AND it into its WHERE: SetClause sets a member
	// whether or not it exists, and its placeholders, which Args binds and
	// ParamCount counts, appear nowhere else. A target row it is false for
	// is one the patch does not apply to; Preconditions says which
	// operation fails there (RenderPreconditions).
	Precondition  string
	Preconditions []Precondition

	// ChangePredicate, rendered with WithChangePredicate, is true for a row
	// the SET clause's patch columns would change: a parenthesized OR of
	// null-safe comparisons of each column with the expression it is set to,
	// over the SET clause's own placeholders and no arguments of its own. It
	// leaves out the auto-timestamp and increments, so ANDed into an UPDATE's
	// WHERE it keeps them from moving on a row the patch leaves as it is.
	// Empty when SetClause is.
	ChangePredicate string

	// Empty is true when neither the column SET clause nor any relation
	// op produced output — the caller can short-circuit with no DB writes.
	Empty bool

	// RelationOps holds parsed-but-unrendered junction operations
	// extracted from the patch. The repository decorator
	// runs these alongside the owner UPDATE in a single transaction:
	// the UPDATE runs first (or is skipped when SetClause is empty),
	// then for each RelationOp the decorator emits a junction
	// DELETE / INSERT bound to the affected owner PKs returned by the
	// UPDATE … RETURNING clause.
	RelationOps []RelationPatchOp
}

// ColumnAssignment is one canonical folded column expression.
type ColumnAssignment struct {
	// Column is the column's name, unquoted; Expression reads the stored
	// column, where it does, quoted and unqualified, so it is the target's
	// own in an UPDATE of one table.
	Column, Expression string
}

// RelationVerb identifies how a relation patch op modifies junction
// membership. The wire-level op name (PatchOpReplace etc.)
// maps 1:1 to a verb at parse time.
type RelationVerb string

const (
	// RelationVerbReplace is full-diff: the relation becomes exactly the
	// supplied set. Implemented as a junction DELETE for rows whose
	// target is not in the new set, followed by an INSERT … ON CONFLICT
	// DO NOTHING for the supplied targets.
	RelationVerbReplace RelationVerb = "replace"
	// RelationVerbAdd unions the supplied targets into the existing set
	// (junction INSERT … ON CONFLICT DO NOTHING).
	RelationVerbAdd RelationVerb = "add"
	// RelationVerbRemove subtracts the supplied targets from the set
	// (junction DELETE WHERE target IN (…)).
	RelationVerbRemove RelationVerb = "remove"
)

// RelationPatchOp is a parsed junction-membership change extracted from
// a JSON Patch document. The repository decorator binds these to the
// owner PKs returned by the owner UPDATE and emits the junction SQL
// inside the same transaction.
type RelationPatchOp struct {
	// Field is the filter-API field name that the patch path resolved
	// to (e.g. "tags" for path "/tags"). Used for error messages and
	// audit logs.
	Field string

	// Schema is the junction descriptor (junction table, source/target
	// columns, optional tenant column).
	Schema *core.RelationSchema

	// Verb is the membership change verb derived from the wire op.
	Verb RelationVerb

	// Values is the list of target IDs the verb applies to: a value per
	// key of a one-column target key, a core.Key per key of a longer one.
	// Empty is valid for RelationVerbReplace (clears the relation) but not
	// for add / remove.
	Values []any
}

// PatchSQLOption configures BuildPatchSQL behavior.
type PatchSQLOption func(*patchSQLConfig)

type patchSQLConfig struct {
	startParam       int
	autoTimestamp    string
	autoTimestampSQL string
	increments       []string
	timeFunc         func() time.Time
	changePredicate  bool
}

// WithStartParam offsets the SET clause's placeholders by n: the first is
// n+1, as with filter.WithStartParam, so one zero-based offset composes
// both builders. Default 0 (first placeholder $1).
func WithStartParam(n int) PatchSQLOption {
	return func(c *patchSQLConfig) { c.startParam = n }
}

// WithAutoTimestamp adds an automatic timestamp column to the SET clause.
func WithAutoTimestamp(col string) PatchSQLOption {
	return func(c *patchSQLConfig) { c.autoTimestamp = col }
}

// WithAutoTimestampSQL is WithAutoTimestamp with the column set to a SQL
// expression — the database's clock, NOW() — instead of a bound time, for a
// repository whose other writes stamp the database's clock: two clocks make
// timestamps incomparable once they skew.
func WithAutoTimestampSQL(col, expr string) PatchSQLOption {
	return func(c *patchSQLConfig) { c.autoTimestamp, c.autoTimestampSQL = col, expr }
}

// WithIncrement also sets col to one more than it holds — a version the
// server moves on every write (once, even if the patch sets it). Each
// WithIncrement adds a column.
func WithIncrement(col string) PatchSQLOption {
	return func(c *patchSQLConfig) { c.increments = append(c.increments, col) }
}

// WithTimeFunc overrides the function used to generate timestamps.
func WithTimeFunc(fn func() time.Time) PatchSQLOption {
	return func(c *patchSQLConfig) { c.timeFunc = fn }
}

// WithChangePredicate also renders PatchSQLResult.ChangePredicate, the
// condition under which the SET clause changes a row.
func WithChangePredicate() PatchSQLOption {
	return func(c *patchSQLConfig) { c.changePredicate = true }
}

// newPatchSQLConfig is the configuration opts make.
func newPatchSQLConfig(opts []PatchSQLOption) *patchSQLConfig {
	cfg := &patchSQLConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

// now is the time the configuration stamps a row with.
func (cfg *patchSQLConfig) now() time.Time {
	if cfg.timeFunc == nil {
		return time.Now().UTC()
	}
	return cfg.timeFunc()
}

// BuildPatchSQL converts a ParsedPatch into a SQL SET clause and a list
// of relation patch ops: the patch composed (ComposePatch) and rendered for
// dialect (ComposedPatch.SQL), under every option either takes.
//
// Scalar-column operations fold, in order, into ONE assignment per column:
// replace/add set the column to the value, remove sets it to NULL,
// array_append appends and an RFC 7396 merge merges into whatever the
// column holds at that point in the sequence. One assignment per operation
// would lose every write but the last to a repeated column (SQLite keeps
// the last duplicate assignment). Operations before a column's final
// replace/add/remove are overwritten and bind no placeholder. Columns appear
// in first-touched order.
//
// Relation ops (PathSchema.Relation != nil) split off into the result's
// RelationOps slice; the repository decorator is responsible for emitting the
// junction SQL bound to the affected owner PKs in the same transaction as the
// owner UPDATE.
//
// WithChangePredicate also compiles the condition under which the patch's
// columns change a row (PatchSQLResult.ChangePredicate), for an UPDATE that
// writes, stamps and counts only the rows it changes.
//
// An add, replace or remove below a JSON document field compiles against
// the stored document (jsonb_set and #- on PostgreSQL, json_set and
// json_remove on SQLite), sibling members kept, with the RFC 6902
// preconditions the patch's earlier operations do not settle in
// Precondition; one they settle as failed is E2077, as the document path
// reports it.
//
// Operations SQL cannot express are rejected, never skipped: test (E2068),
// move/copy, an array element and a member whose name reads as an array
// index where the document's type does not make its parent an object
// (E2070). Apply those through the typed document path instead. What the
// composition refuses is refused before anything is rendered, so a patch
// every store refuses is refused alike whatever dialect it is rendered for.
func BuildPatchSQL(patch *ParsedPatch, dialect SQLDialect, opts ...PatchSQLOption) (*PatchSQLResult, error) {
	cfg := newPatchSQLConfig(opts)
	c, err := compose(patch, cfg)
	if err != nil {
		return nil, err
	}
	return c.render(dialect, cfg)
}

// SQL renders the update for d with the rendering options, WithStartParam
// and WithChangePredicate: BuildPatchSQL's result for the patch and the
// options it was composed with. The composition options are ComposePatch's,
// and SQL refuses them.
func (c *ComposedPatch) SQL(d SQLDialect, opts ...PatchSQLOption) (*PatchSQLResult, error) {
	cfg := newPatchSQLConfig(opts)
	if cfg.autoTimestamp != "" || len(cfg.increments) > 0 || cfg.timeFunc != nil {
		return nil, errors.New("patch: ComposedPatch.SQL takes no composition option; pass WithAutoTimestamp, WithAutoTimestampSQL, WithIncrement and WithTimeFunc to ComposePatch")
	}
	return c.render(d, cfg)
}

// render is the update's SQL for d, its placeholders after cfg.startParam.
func (c *ComposedPatch) render(d SQLDialect, cfg *patchSQLConfig) (*PatchSQLResult, error) {
	if c.Empty() {
		empty := &PatchSQLResult{Empty: true}
		return empty, nil
	}
	b := &setBuilder{dialect: d, next: cfg.startParam + 1}
	var changes []string
	for _, ch := range c.columns {
		write, err := b.columnExpr(ch)
		if err != nil {
			return nil, err
		}
		b.assignments = append(b.assignments, ColumnAssignment{Column: ch.column, Expression: write.expr})
		if cfg.changePredicate {
			changed, err := changedSQL(d, d.QuoteIdent(ch.column), write)
			if err != nil {
				return nil, dialectError("change predicate", fieldPath(ch.ops[0]), err)
			}
			changes = append(changes, changed)
		}
	}
	var changePredicate string
	if len(changes) > 0 {
		changePredicate = "(" + strings.Join(changes, " OR ") + ")"
	}
	precondition, _, err := RenderPreconditions(d, c.preconditions, b.bind)
	if err != nil {
		return nil, err
	}
	if c.stamp != "" {
		value := c.stampSQL
		if value == "" {
			// Canonical text: SQLite compares it as text, pgx binds it to
			// timestamptz.
			value = b.bind(core.FormatTimestamp(c.stampAt))
		}
		b.assignments = append(b.assignments, ColumnAssignment{Column: c.stamp, Expression: value})
	}
	for _, col := range c.increments {
		b.assignments = append(b.assignments, ColumnAssignment{Column: col, Expression: d.QuoteIdent(col) + " + 1"})
	}

	clauses := make([]string, len(b.assignments))
	for i, assignment := range b.assignments {
		clauses[i] = d.QuoteIdent(assignment.Column) + " = " + assignment.Expression
	}
	return &PatchSQLResult{
		SetClause:       strings.Join(clauses, ", "),
		Assignments:     b.assignments,
		Args:            b.args,
		ParamCount:      b.next - cfg.startParam - 1,
		ChangePredicate: changePredicate,
		Precondition:    precondition,
		Preconditions:   slices.Clone(c.preconditions),
		Empty:           len(b.assignments) == 0 && len(c.relations) == 0,
		RelationOps:     slices.Clone(c.relations),
	}, nil
}

// changedSQL is the null-safe condition, one or more terms to OR, that col
// written as w reads back other than it does: set to NULL, that col holds
// a value.
func changedSQL(d SQLDialect, col string, w columnWrite) (string, error) {
	switch {
	case w.members:
		return d.ChangedDocumentCondition(col, w.expr)
	case w.expr == "NULL":
		return col + " IS NOT NULL", nil
	}
	var t core.TypeInfo
	if w.schema != nil {
		t = pathType(w.schema)
	}
	return d.ChangedCondition(col, w.expr, t)
}

// dialectError is err, which the dialect returned rendering what of the
// operation at path: a refusal (SQLDialect) as that path's E2070, anything
// else wrapped with what and path.
func dialectError(what, path string, err error) error {
	if !errors.Is(err, core.CodeOpNotSQLCompilable) {
		return fmt.Errorf("%s for %s: %w", what, path, err)
	}
	why := err.Error()
	refusal, ok := errors.AsType[*core.ParseError](err)
	if ok {
		why = refusal.Message
	}
	pe := core.NewError(ECodeOpNotSQLCompilable, what+" for "+path+": "+why+" — apply via ApplyValidatedPatch",
		core.WithPath(path), core.WithCause(err))
	return pe
}

// fieldPath is the path of the field op writes, or a member of.
func fieldPath(op *PatchOperation) string {
	tokens, err := core.ParsePointer(op.Path)
	if err != nil || len(tokens) == 0 {
		return op.Path
	}
	return core.FormatPointer(tokens[0])
}

// serverColumns are the columns the server writes on every update: the
// auto-timestamp and the counters, each an identifier named once.
func serverColumns(cfg *patchSQLConfig) (map[string]bool, error) {
	cols := map[string]bool{}
	add := func(col string) error {
		if !core.IsIdentifier(col) {
			return fmt.Errorf("patch: server column %q is not an identifier", col)
		}
		if cols[col] {
			return fmt.Errorf("patch: server column %q is named twice", col)
		}
		cols[col] = true
		return nil
	}
	if cfg.autoTimestamp != "" {
		if err := add(cfg.autoTimestamp); err != nil {
			return nil, err
		}
	}
	for _, col := range cfg.increments {
		if err := add(col); err != nil {
			return nil, err
		}
	}
	return cols, nil
}

// checkSQLCompilable rejects operations a column SET clause cannot express.
func checkSQLCompilable(op *PatchOperation) error {
	switch op.Op {
	case PatchOpTest:
		// A caller sending [test, replace] means the replace to be
		// conditional, and the compiled UPDATE cannot make it so: dropping
		// the test would leave that intent unenforced, so it is refused.
		return core.NewError(ECodeTestOpSQL, fmt.Sprintf("test op for %s cannot be compiled to SQL; conditional update is not supported — verify with a read or use ApplyValidatedPatch", op.Path),
			core.WithPath(op.Path))
	case PatchOpMove, PatchOpCopy:
		return core.NewError(ECodeOpNotSQLCompilable, fmt.Sprintf("%s op for %s cannot be compiled to SQL — apply via ApplyValidatedPatch", op.Op, op.Path),
			core.WithPath(op.Path))
	case PatchOpReplace, PatchOpAdd, PatchOpRemove, PatchOpArrayAppend:
	default:
		return core.NewError(ECodeOpNotSQLCompilable, fmt.Sprintf("op %q for %s cannot be compiled to SQL", op.Op, op.Path),
			core.WithPath(op.Path))
	}
	if op.Op == PatchOpArrayAppend && strings.Contains(strings.TrimPrefix(op.Path, "/"), "/") {
		return notCompilable(op, fmt.Sprintf("array_append on %s, below a field, compiles to SQL only for a native array column", op.Path))
	}
	return nil
}

// SQLDialect is the dialect BuildPatchSQL and RenderPreconditions write a
// patch's SQL for, and what a SQL provider implements for it: placeholders
// and quoting, value binding for composite types, and one method per piece
// of SQL whose form differs between databases. patch decides what a patch
// writes; the dialect decides how its database spells it, so a new provider
// is one type and patch names no database. The parse/filter dialects
// implement it: PostgreSQL over jsonb and native arrays, SQLite over JSON
// text.
//
// Every method that binds takes bind, which appends one argument and
// returns its placeholder; a method binds in the order its SQL reads, so
// SQL text and arguments are built together and cannot disagree. A member
// is a JSON Pointer's tokens below the column, each an object member's
// name, never an array index, and valid UTF-8 without U+0000, which
// neither database holds in text: ParsePatch refuses a path holding it as
// E2075 (core.CheckText). doc is the expression MemberDocument renders, or
// one SetMember or RemoveMember returned for it.
//
// A method refuses what its database cannot write or test with a
// *core.ParseError whose Code is core.CodeOpNotSQLCompilable and whose
// Message says why: BuildPatchSQL reports it as the operation's E2070,
// naming its path, so the patch can be applied with ApplyValidatedPatch
// instead. Any other error is a failure to render the statement.
type SQLDialect interface {
	dialect.Dialect

	// MarshalValue converts a value to the form the dialect binds for a
	// column of type t: a PostgreSQL TEXT[] column takes a Go slice, a
	// SQLite array column JSON text. Requiring it in the signature makes
	// the wrong binding uncompilable instead of a runtime "malformed array
	// literal".
	MarshalValue(value any, t core.TypeInfo) (any, error)

	// ChangedCondition is the null-safe condition, one or more terms to
	// OR, that column, written whole, set to expr reads back other than it
	// does; t is the column's type and declared storage as the patch's
	// path declares them. expr is never the NULL literal. A false
	// "changed" costs a write, a false "unchanged" drops one, so a type
	// whose equality is coarser than what a read returns compares by that.
	ChangedCondition(column, expr string, t core.TypeInfo) (string, error)
	// ChangedDocumentCondition is ChangedCondition for a JSON document
	// column whose members the patch writes over the stored document:
	// doc is the document the member methods built from MemberDocument's,
	// so it has the type MemberDocument reads the column as, whatever JSON
	// type, or text, the column is declared.
	ChangedDocumentCondition(column, doc string) (string, error)

	// MergePatch merges the RFC 7396 patch object into the JSON document
	// doc, stored as storage: patch members replace, nested objects merge,
	// nulls delete, and a target that is not an object, NULL included,
	// merges as {} (RFC 7396 §2).
	MergePatch(doc string, storage core.SQLType, patch map[string]any, bind func(any) string) (string, error)

	// ArrayAppend appends element to the native array expression array, a
	// NULL array as an empty one, so the first append to a fresh column
	// yields a one-element array.
	ArrayAppend(array string, element any, bind func(any) string) (string, error)

	// MemberDocument is the JSON document column expression expr holds,
	// as the member methods read and write it.
	MemberDocument(expr string) (string, error)
	// SetMember sets member of doc to value, a JSON text, whether or not
	// the member exists; its parent must be an object, which a
	// precondition holds the row to (IsObject).
	SetMember(doc string, member []string, value json.RawMessage, bind func(any) string) (string, error)
	// RemoveMember removes member from doc.
	RemoveMember(doc string, member []string, bind func(any) string) (string, error)
	// HasMember is true of a row whose doc holds member, and never NULL.
	HasMember(doc string, member []string, bind func(any) string) (string, error)
	// IsObject is true of a row whose doc's member, or doc itself when
	// member is empty, is an object, and never NULL.
	IsObject(doc string, member []string, bind func(any) string) (string, error)
}

// setBuilder accumulates SET clauses and their bound arguments. Placeholders
// are numbered as they are bound, so Args is always in placeholder order.
type setBuilder struct {
	dialect     SQLDialect
	next        int
	assignments []ColumnAssignment
	args        []any
}

func (b *setBuilder) bind(v any) string {
	ph := b.dialect.Placeholder(b.next)
	b.next++
	b.args = append(b.args, v)
	return ph
}

// columnWrite is what one column's operations fold to: the expression the
// SET clause assigns, and what the change predicate compares the column
// with it as.
type columnWrite struct {
	expr string
	// schema is the path schema of a column written whole, which it is
	// compared as (SQLDialect.ChangedCondition). members marks a document
	// whose members are written over the stored one, compared as such
	// (SQLDialect.ChangedDocumentCondition).
	schema  *PathSchema
	members bool
}

// columnExpr renders one column's change as the SQL expression the SET
// clause assigns it: a value bound as its path declares it, or NULL; a
// merge into, appends to or member writes over the stored column.
func (b *setBuilder) columnExpr(ch *columnChange) (columnWrite, error) {
	col := b.dialect.QuoteIdent(ch.column)
	switch ch.kind {
	case changeMerge:
		op := ch.ops[0]
		expr, err := b.dialect.MergePatch(col, op.Schema.SQLType, ch.merge, b.bind)
		if err != nil {
			return columnWrite{}, dialectError(string(op.Op), op.Path, err)
		}
		merged := columnWrite{expr: expr, schema: ch.schema}
		return merged, nil
	case changeAppend:
		expr := col
		for _, op := range ch.appends {
			var err error
			expr, err = b.dialect.ArrayAppend(expr, op.Value, b.bind)
			if err != nil {
				return columnWrite{}, dialectError(string(op.Op), op.Path, err)
			}
		}
		appended := columnWrite{expr: expr, schema: ch.schema}
		return appended, nil
	case changeMembers:
		doc, err := b.dialect.MemberDocument(col)
		if err != nil {
			return columnWrite{}, dialectError("document", fieldPath(ch.ops[0]), err)
		}
		for _, e := range ch.edits {
			if e.op.Op == PatchOpRemove {
				doc, err = b.dialect.RemoveMember(doc, e.member, b.bind)
			} else {
				doc, err = b.dialect.SetMember(doc, e.member, e.value, b.bind)
			}
			if err != nil {
				return columnWrite{}, dialectError(string(e.op.Op), e.op.Path, err)
			}
		}
		members := columnWrite{expr: doc, members: true}
		return members, nil
	}
	if ch.value == nil {
		cleared := columnWrite{expr: "NULL", schema: ch.schema}
		return cleared, nil
	}
	val, err := b.dialect.MarshalValue(ch.value, pathType(ch.base.Schema))
	if err != nil {
		return columnWrite{}, fmt.Errorf("marshaling value for %s: %w", ch.base.Path, err)
	}
	bound := columnWrite{expr: b.bind(val), schema: ch.schema}
	return bound, nil
}

// buildRelationPatchOp turns a parsed relation op into a RelationPatchOp.
// The parser decided its value: a list of keys, at least one for an add or
// remove (relationValue).
func buildRelationPatchOp(op *PatchOperation) (RelationPatchOp, error) {
	verb, err := relationVerbFromOp(op.Op)
	if err != nil {
		return RelationPatchOp{}, core.NewError(ECodeRelationVerbNotAllowed, fmt.Sprintf("op %q not allowed on relation path %q", op.Op, op.Path),
			core.WithPath(op.Path))
	}
	values, _ := core.AnySlice(op.Value)
	return RelationPatchOp{
		Field:  strings.TrimPrefix(op.Path, "/"),
		Schema: op.Schema.Relation,
		Verb:   verb,
		Values: values,
	}, nil
}

func relationVerbFromOp(op PatchOp) (RelationVerb, error) {
	switch op {
	case PatchOpReplace:
		return RelationVerbReplace, nil
	case PatchOpAdd:
		return RelationVerbAdd, nil
	case PatchOpRemove:
		return RelationVerbRemove, nil
	default:
		return "", fmt.Errorf("op %q has no relation-verb mapping", op)
	}
}

func columnName(op *PatchOperation) (string, error) {
	if op.Schema != nil && op.Schema.SQLColumn != "" {
		return op.Schema.SQLColumn, nil
	}
	// The request's path text is never a SQL identifier.
	return "", fmt.Errorf("path %s: %w", op.Path, ErrSQLColumnMissing)
}
