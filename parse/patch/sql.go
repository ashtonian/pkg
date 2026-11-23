package patch

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
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

// BuildPatchSQL converts a ParsedPatch into a SQL SET clause and a list
// of relation patch ops.
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
// (E2070). Apply those through the typed document path instead.
func BuildPatchSQL(patch *ParsedPatch, dialect SQLDialect, opts ...PatchSQLOption) (*PatchSQLResult, error) {
	cfg := &patchSQLConfig{
		timeFunc: func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(cfg)
	}

	server, err := serverColumns(cfg)
	if err != nil {
		return nil, err
	}
	if err := checkParsed(patch); err != nil {
		return nil, err
	}
	if len(patch.Operations) == 0 {
		return &PatchSQLResult{Empty: true}, nil
	}

	var (
		columns     []string // first-touched order
		columnOps   = map[string][]*PatchOperation{}
		relationOps []RelationPatchOp
		index       = make(map[*PatchOperation]int, len(patch.Operations))
	)
	for i, op := range patch.Operations {
		index[op] = i
		if op.Schema != nil && op.Schema.Relation != nil {
			rop, err := buildRelationPatchOp(op)
			if err != nil {
				return nil, err
			}
			relationOps = append(relationOps, rop)
			continue
		}
		if err := checkSQLCompilable(op); err != nil {
			return nil, err
		}
		if len(memberTokens(op)) > 0 {
			if err := checkMemberPath(patch.schema, op); err != nil {
				return nil, err
			}
		}
		col, err := columnName(op)
		if err != nil {
			return nil, err
		}
		if server[col] {
			// The server owns its stamped and counted columns; its value wins.
			continue
		}
		if _, seen := columnOps[col]; !seen {
			columns = append(columns, col)
		}
		columnOps[col] = append(columnOps[col], op)
	}

	b := &setBuilder{dialect: dialect, next: cfg.startParam + 1, index: index}
	var changes []string
	for _, col := range columns {
		ops := columnOps[col]
		expr, changeSchema, err := b.columnExpr(col, ops)
		if err != nil {
			return nil, err
		}
		b.assignments = append(b.assignments, ColumnAssignment{Column: col, Expression: expr})
		if cfg.changePredicate {
			change, err := changedSQL(dialect, dialect.QuoteIdent(col), expr, changeSchema)
			if err != nil {
				return nil, fmt.Errorf("change predicate for %s: %w", col, err)
			}
			changes = append(changes, change)
		}
	}
	var changePredicate string
	if len(changes) > 0 {
		changePredicate = "(" + strings.Join(changes, " OR ") + ")"
	}
	// Columns fold one after another, so the preconditions are put back in
	// patch order: the first one a row fails is the operation that fails.
	slices.SortFunc(b.preconditions, func(x, y Precondition) int { return cmp.Compare(x.Op, y.Op) })
	precondition, _, err := RenderPreconditions(dialect, b.preconditions, b.bind)
	if err != nil {
		return nil, err
	}

	if len(b.assignments) > 0 {
		if cfg.autoTimestamp != "" {
			value := cfg.autoTimestampSQL
			if value == "" {
				// Canonical text: SQLite compares it as text, pgx binds it to
				// timestamptz.
				value = b.bind(core.FormatTimestamp(cfg.timeFunc()))
			}
			b.assignments = append(b.assignments, ColumnAssignment{Column: cfg.autoTimestamp, Expression: value})
		}
		for _, col := range cfg.increments {
			b.assignments = append(b.assignments, ColumnAssignment{Column: col, Expression: dialect.QuoteIdent(col) + " + 1"})
		}
	}

	clauses := make([]string, len(b.assignments))
	for i, assignment := range b.assignments {
		clauses[i] = dialect.QuoteIdent(assignment.Column) + " = " + assignment.Expression
	}
	return &PatchSQLResult{
		SetClause:       strings.Join(clauses, ", "),
		Assignments:     b.assignments,
		Args:            b.args,
		ParamCount:      b.next - cfg.startParam - 1,
		ChangePredicate: changePredicate,
		Precondition:    precondition,
		Preconditions:   b.preconditions,
		Empty:           len(b.assignments) == 0 && len(relationOps) == 0,
		RelationOps:     relationOps,
	}, nil
}

// changedSQL is the null-safe condition, one or more terms to OR, that col
// set to expr reads back other than it does: a false "changed" costs a
// write, a false "unchanged" would drop one.
func changedSQL(d dialect.Dialect, col, expr string, schema *PathSchema) (string, error) {
	if expr == "NULL" {
		return col + " IS NOT NULL", nil
	}
	switch k := dialect.KindOf(d); k {
	case dialect.KindSQLite:
		// IS NOT is SQLite's null-safe inequality on every version that runs
		// the SQL (IS DISTINCT FROM is a 3.39 alias). BINARY overrides a
		// NOCASE or RTRIM column and keeps its affinity, which converts the
		// value for the comparison as it does on store.
		return col + " COLLATE BINARY IS NOT " + expr, nil
	case dialect.KindPostgres:
		switch {
		case schema != nil && schema.SQLType == core.SQLJSON:
			// json has no equality operator, and jsonb's text is canonical:
			// key order and whitespace do not count, a number's written form
			// does. The cast to json types the placeholder as the column.
			return col + `::jsonb::text COLLATE "C" IS DISTINCT FROM (` + expr + ")::json::jsonb::text", nil
		case schema != nil && schema.DataType == core.FieldTypeDecimal:
			// A decimal's value has no scale (core.DecimalText binds 1.50 as
			// 1.5), and a numeric(p,s) column pads every value to s: its text
			// would differ from the bound value's on every write.
			return col + " IS DISTINCT FROM " + expr, nil
		}
		// A type's equality can be coarser than the text a read returns — an
		// interval's units, a float's zero sign, citext, a nondeterministic
		// collation, a jsonb number's form — so the text decides. The
		// equality goes first because PostgreSQL types a placeholder at its
		// first use and the WHERE precedes the SET clause: it types the
		// placeholder as the column (an enum, a domain), where a first use as
		// text would not.
		return col + " IS DISTINCT FROM " + expr + " OR " + col + `::text COLLATE "C" IS DISTINCT FROM (` + expr + ")::text", nil
	default:
		return "", dialect.Unsupported(k, "a change predicate")
	}
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
		return &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodeTestOpSQL.String(),
			Message: fmt.Sprintf("test op for %s cannot be compiled to SQL; conditional update is not supported — verify with a read or use ApplyValidatedPatch", op.Path),
			Path:    op.Path,
		}
	case PatchOpMove, PatchOpCopy:
		return &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodeOpNotSQLCompilable.String(),
			Message: fmt.Sprintf("%s op for %s cannot be compiled to SQL — apply via ApplyValidatedPatch", op.Op, op.Path),
			Path:    op.Path,
		}
	case PatchOpReplace, PatchOpAdd, PatchOpRemove, PatchOpArrayAppend:
	default:
		return &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodeOpNotSQLCompilable.String(),
			Message: fmt.Sprintf("op %q for %s cannot be compiled to SQL", op.Op, op.Path),
			Path:    op.Path,
		}
	}
	if op.Op == PatchOpArrayAppend && strings.Contains(strings.TrimPrefix(op.Path, "/"), "/") {
		return notCompilable(op, fmt.Sprintf("array_append on %s, below a field, compiles to SQL only for a native array column", op.Path))
	}
	return nil
}

// SQLDialect is the dialect surface BuildPatchSQL needs: placeholders and a
// name for dialect-specific SQL, plus value binding for composite types (a
// PostgreSQL TEXT[] column takes a Go slice, a SQLite array column takes JSON
// text). Requiring MarshalValue in the signature makes the wrong binding
// uncompilable instead of a runtime "malformed array literal". The
// parse/filter dialects implement it.
type SQLDialect interface {
	dialect.Dialect
	MarshalValue(value any, t core.TypeInfo) (any, error)
}

// setBuilder accumulates SET clauses and their bound arguments. Placeholders
// are numbered as they are bound, so Args is always in placeholder order.
type setBuilder struct {
	dialect     SQLDialect
	next        int
	assignments []ColumnAssignment
	args        []any
	// index is each operation's index in the patch; preconditions are the
	// member operations' the stored rows decide.
	index         map[*PatchOperation]int
	preconditions []Precondition
}

func (b *setBuilder) bind(v any) string {
	ph := b.dialect.Placeholder(b.next)
	b.next++
	b.args = append(b.args, v)
	return ph
}

// columnExpr folds one column's ordered operations into a single SQL
// expression, and returns the path schema its change predicate compares the
// column as. A merge patch writes a column with one operation, the only kind
// that merges, so a merge compiles against the stored column. Operations on
// members of a document fold in documentExpr. Other JSON Patch operations
// fold: the last whole-value operation (replace/add/remove) sets the base
// and earlier ones are overwritten and never bound, and appends after it
// fold onto its value in Go, so one value is bound; appends with no base
// compile against the stored column.
func (b *setBuilder) columnExpr(col string, ops []*PatchOperation) (string, *PathSchema, error) {
	last := ops[len(ops)-1].Schema
	if slices.ContainsFunc(ops, func(op *PatchOperation) bool { return op.Merge }) {
		if len(ops) != 1 {
			return "", nil, fmt.Errorf("patch: column %s: a merge shares its column with another operation, which no parser produces", col)
		}
		expr, err := mergeSQL(b.dialect, b.dialect.QuoteIdent(col), ops[0].Schema.SQLType, ops[0].Value, b.bind)
		if err != nil {
			return "", nil, fmt.Errorf("%s for %s: %w", ops[0].Op, ops[0].Path, err)
		}
		return expr, last, nil
	}
	if slices.ContainsFunc(ops, func(op *PatchOperation) bool { return len(memberTokens(op)) > 0 }) {
		return b.documentExpr(col, ops)
	}
	start := -1
	for i, op := range slices.Backward(ops) {
		if op.Op != PatchOpArrayAppend {
			start = i
			break
		}
	}
	later := ops[start+1:]
	for _, op := range later {
		if op.Schema.DataType != "" && op.Schema.DataType != core.FieldTypeArray {
			// A JSON-document array (jsonb on PostgreSQL) has no native
			// array_append; the document path applies it.
			return "", nil, &core.ParseError{
				Kind: core.ParseErrorKindPatch, Code: ECodeOpNotSQLCompilable.String(),
				Message: fmt.Sprintf("array_append on %s (a %s field) compiles to SQL only for native array columns — apply via ApplyValidatedPatch", op.Path, op.Schema.DataType),
				Path:    op.Path,
			}
		}
	}
	if start < 0 {
		expr := b.dialect.QuoteIdent(col)
		for _, op := range later {
			var err error
			if expr, err = arrayAppendSQL(b.dialect, expr, op.Value, b.bind); err != nil {
				return "", nil, fmt.Errorf("%s for %s: %w", op.Op, op.Path, err)
			}
		}
		return expr, last, nil
	}

	base := ops[start]
	var value any
	if base.Op != PatchOpRemove {
		value = base.Value
	}
	for _, op := range later {
		var elems []any
		if value != nil {
			arr, ok := core.AnySlice(value)
			if !ok {
				return "", nil, fmt.Errorf("array append for %s: base value is %T, not an array", base.Path, value)
			}
			elems = slices.Clone(arr)
		}
		value = append(elems, op.Value)
	}
	if value == nil {
		if value = base.Schema.clearedValue(); value == nil {
			return "NULL", last, nil
		}
	}
	val, err := marshalIfComplex(value, base.Schema, b.dialect)
	if err != nil {
		return "", nil, fmt.Errorf("marshaling value for %s: %w", base.Path, err)
	}
	return b.bind(val), last, nil
}

// mergeSQL returns the expression merging an RFC 7396 patch object into the
// JSON document expr, stored as storage: patch members replace, nested
// objects merge, nulls delete, and a target that is not an object merges as
// {} (RFC 7396 §2). SQLite implements RFC 7396 as json_patch; PostgreSQL has
// none, so the patch compiles level by level (pgMerge) over jsonb. A json
// column (core.SQLJSON) is read as jsonb, since its cast to jsonb applies on
// assignment but not to a function's argument, and the jsonb result assigns
// back to it.
func mergeSQL(d dialect.Dialect, expr string, storage core.SQLType, patch any, bind func(any) string) (string, error) {
	obj, ok := patch.(map[string]any)
	if !ok {
		return "", fmt.Errorf("merge value is %T, not an object", patch)
	}
	switch k := dialect.KindOf(d); k {
	case dialect.KindSQLite:
		doc, err := json.Marshal(obj)
		if err != nil {
			return "", err
		}
		return "json_patch(COALESCE(" + expr + ", '{}'), " + bind(string(doc)) + ")", nil
	case dialect.KindPostgres:
		if storage == core.SQLJSON {
			expr += "::jsonb"
		}
		levels := 0
		return pgMerge(expr, obj, bind, &levels)
	default:
		return "", dialect.Unsupported(k, "a merge patch")
	}
}

// pgMerge compiles the merge of patch into the document target: the target
// when it is an object (else {}), minus the deleted keys, joined with one
// bound jsonb object holding the scalar and array members and one
// two-argument jsonb_build_object per nested object. A level with nested
// objects names its target once in a scalar subquery, and each nested level
// reads its own target from that name, so target is evaluated once, the SQL
// and its arguments grow linearly with the patch, and no call nears
// PostgreSQL's 100-argument limit however wide a level is.
func pgMerge(target string, patch map[string]any, bind func(any) string, levels *int) (string, error) {
	self := "COALESCE(jsonb_path_query_first(" + target + `, 'strict $ ? (@.type() == "object")'), '{}'::jsonb)`
	alias := ""
	if hasNestedObject(patch) {
		alias = fmt.Sprintf("_merge%d", *levels)
		*levels++
		self, target = alias+".doc", self
	}
	var deleted []string
	members := map[string]any{}
	var nested []string
	for _, k := range slices.Sorted(maps.Keys(patch)) {
		switch v := patch[k].(type) {
		case nil:
			deleted = append(deleted, k)
		case map[string]any:
			key := bind(k) + "::text"
			sub, err := pgMerge("("+self+" -> "+key+")", v, bind, levels)
			if err != nil {
				return "", err
			}
			nested = append(nested, "jsonb_build_object("+key+", "+sub+")")
		default:
			members[k] = v
		}
	}
	var expr strings.Builder
	expr.WriteString(self)
	if len(deleted) > 0 {
		expr.WriteString(" - ")
		expr.WriteString(bind(deleted))
		expr.WriteString("::text[]")
	}
	if len(members) > 0 {
		doc, err := json.Marshal(members)
		if err != nil {
			return "", fmt.Errorf("merge members: %w", err)
		}
		expr.WriteString(" || ")
		expr.WriteString(bind(string(doc)))
		expr.WriteString("::jsonb")
	}
	for _, n := range nested {
		expr.WriteString(" || ")
		expr.WriteString(n)
	}
	if alias == "" {
		return "(" + expr.String() + ")", nil
	}
	return "(SELECT " + expr.String() + " FROM (SELECT " + target + " AS doc) AS " + alias + ")", nil
}

func hasNestedObject(patch map[string]any) bool {
	for _, v := range patch {
		if _, ok := v.(map[string]any); ok {
			return true
		}
	}
	return false
}

// buildRelationPatchOp turns a parsed relation op into a RelationPatchOp.
// The parser decided its value: a list of keys, at least one for an add or
// remove (relationValue).
func buildRelationPatchOp(op *PatchOperation) (RelationPatchOp, error) {
	verb, err := relationVerbFromOp(op.Op)
	if err != nil {
		return RelationPatchOp{}, &core.ParseError{
			Kind:    core.ParseErrorKindPatch,
			Code:    ECodeRelationVerbNotAllowed.String(),
			Message: fmt.Sprintf("op %q not allowed on relation path %q", op.Op, op.Path),
			Path:    op.Path,
		}
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

// arrayAppendSQL returns the dialect-specific expression appending element
// to the array expression expr. NULL coalesces to the empty container so the
// first append to a fresh NULL column yields a one-element array.
func arrayAppendSQL(d dialect.Dialect, expr string, element any, bind func(any) string) (string, error) {
	switch k := dialect.KindOf(d); k {
	case dialect.KindPostgres:
		// array_append preserves element order; the UPDATE's row lock
		// serialises concurrent appenders without losing values.
		// The untyped '{}' takes the column's array type, so BIGINT[] and
		// UUID[] columns append as well as TEXT[].
		return fmt.Sprintf("array_append(COALESCE(%s, '{}'), %s)", expr, bind(element)), nil
	case dialect.KindSQLite:
		// SQLite stores arrays as JSON TEXT; '$[#]' appends. The element is
		// bound as its JSON encoding — the one a full replace writes — and
		// read back through json(): a bound SQL value would be inserted as
		// a SQL scalar, so a Go true would land as the integer 1.
		data, err := json.Marshal(element)
		if err != nil {
			return "", fmt.Errorf("encode the appended element: %w", err)
		}
		return fmt.Sprintf("json_insert(COALESCE(%s, '[]'), '$[#]', json(%s))", expr, bind(string(data))), nil
	default:
		return "", dialect.Unsupported(k, "array_append")
	}
}

func marshalIfComplex(value any, schema *PathSchema, dialect SQLDialect) (any, error) {
	if schema == nil || value == nil {
		return value, nil
	}
	t := core.TypeInfo{Type: schema.DataType, SQLType: schema.SQLType}
	if schema.DataType == core.FieldTypeArray && schema.GoType != nil {
		t.Elem = core.TypeOf(schema.GoType).Elem
	}
	return dialect.MarshalValue(value, t)
}
