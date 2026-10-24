package patch

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/dialect"
	"github.com/ashtonian/pkg/parse/internal/jsonmerge"
)

// PatchSQLResult holds the generated SQL SET clause and arguments.
type PatchSQLResult struct {
	// SetClause is the comma-separated `col = $N` fragment ready to drop
	// into UPDATE … SET. Empty when the patch only touches relation
	// fields (junction-only patches).
	SetClause string

	// Args are the bound values for SetClause placeholders, in $N order.
	Args []any

	// ParamCount is the number of placeholders consumed by SetClause.
	// Callers append filter params starting at startParam + ParamCount.
	ParamCount int

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

	// Values is the list of target IDs the verb applies to. Empty is
	// valid for RelationVerbReplace (clears the relation) but not for
	// add / remove.
	Values []any
}

// PatchSQLOption configures BuildPatchSQL behavior.
type PatchSQLOption func(*patchSQLConfig)

type patchSQLConfig struct {
	startParam       int
	autoTimestamp    string
	autoTimestampSQL string
	timeFunc         func() time.Time
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

// WithTimeFunc overrides the function used to generate timestamps.
func WithTimeFunc(fn func() time.Time) PatchSQLOption {
	return func(c *patchSQLConfig) { c.timeFunc = fn }
}

// BuildPatchSQL converts a ParsedPatch into a SQL SET clause and a list
// of relation patch ops.
//
// Scalar-column operations fold, in order, into ONE assignment per column
// : replace/add set the column to the value, remove sets it to
// NULL, array_append appends and an RFC 7396 merge merges into whatever the
// column holds at that point in the sequence. Emitting one assignment per
// operation lost every write but the last to a repeated column (SQLite keeps
// the last duplicate assignment). Operations before a column's final
// replace/add/remove are overwritten and bind no placeholder. Columns appear
// in first-touched order.
//
// Relation ops (PathSchema.Relation != nil) split off into the result's
// RelationOps slice; the repository decorator is responsible for emitting the
// junction SQL bound to the affected owner PKs in the same transaction as the
// owner UPDATE.
//
// Operations SQL cannot express are rejected, never skipped: test (E2068),
// move/copy and nested JSON Pointer paths (E2070). Apply those through the
// typed document path instead.
func BuildPatchSQL(patch *ParsedPatch, dialect SQLDialect, opts ...PatchSQLOption) (*PatchSQLResult, error) {
	cfg := &patchSQLConfig{
		timeFunc: func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		opt(cfg)
	}

	if patch == nil || len(patch.Operations) == 0 {
		return &PatchSQLResult{Empty: true}, nil
	}

	var (
		columns     []string // first-touched order
		columnOps   = map[string][]*PatchOperation{}
		relationOps []RelationPatchOp
	)
	for _, op := range patch.Operations {
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
		col, err := columnName(op)
		if err != nil {
			return nil, err
		}
		if cfg.autoTimestamp != "" && col == cfg.autoTimestamp {
			// The server owns the auto-timestamp column; its value wins.
			continue
		}
		if _, seen := columnOps[col]; !seen {
			columns = append(columns, col)
		}
		columnOps[col] = append(columnOps[col], op)
	}

	b := &setBuilder{dialect: dialect, next: cfg.startParam + 1}
	for _, col := range columns {
		expr, err := b.columnExpr(col, columnOps[col])
		if err != nil {
			return nil, err
		}
		b.clauses = append(b.clauses, col+" = "+expr)
	}

	if cfg.autoTimestamp != "" && len(b.clauses) > 0 {
		value := cfg.autoTimestampSQL
		if value == "" {
			// Canonical text: SQLite compares it as text, pgx binds it to
			// timestamptz.
			value = b.bind(core.FormatTimestamp(cfg.timeFunc()))
		}
		b.clauses = append(b.clauses, cfg.autoTimestamp+" = "+value)
	}

	return &PatchSQLResult{
		SetClause:   strings.Join(b.clauses, ", "),
		Args:        b.args,
		ParamCount:  b.next - cfg.startParam - 1,
		Empty:       len(b.clauses) == 0 && len(relationOps) == 0,
		RelationOps: relationOps,
	}, nil
}

// checkSQLCompilable rejects operations a column SET clause cannot express.
func checkSQLCompilable(op *PatchOperation) error {
	switch op.Op {
	case PatchOpTest:
		// Silently dropping test ops left optimistic-concurrency intent
		// unenforced — a caller sending [test, replace] believed the replace
		// was conditional. Conditional UPDATE is not
		// supported server-side; reject loudly.
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
	if strings.Contains(strings.TrimPrefix(op.Path, "/"), "/") {
		// A column assignment replaces the whole column; compiling a nested
		// pointer to `SET col = $1` would overwrite every sibling key.
		return &core.ParseError{
			Kind: core.ParseErrorKindPatch, Code: ECodeOpNotSQLCompilable.String(),
			Message: fmt.Sprintf("nested path %s cannot be compiled to a column assignment — apply via ApplyValidatedPatch", op.Path),
			Path:    op.Path,
		}
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
	dialect SQLDialect
	next    int
	clauses []string
	args    []any
}

func (b *setBuilder) bind(v any) string {
	ph := b.dialect.Placeholder(b.next)
	b.next++
	b.args = append(b.args, v)
	return ph
}

// columnExpr folds one column's ordered operations into a single SQL
// expression. The last whole-value operation (replace/add/remove) sets the
// base and earlier operations are overwritten and never bound. Merges and
// array_appends after a base fold onto its value in Go, so one value is
// bound; only merges and appends against the stored column compile to SQL.
func (b *setBuilder) columnExpr(col string, ops []*PatchOperation) (string, error) {
	start := -1
	for i, op := range slices.Backward(ops) {
		if !op.Merge && op.Op != PatchOpArrayAppend {
			start = i
			break
		}
	}
	later := ops[start+1:]
	for _, op := range later {
		if op.Op == PatchOpArrayAppend && op.Schema != nil && op.Schema.DataType != "" && op.Schema.DataType != core.FieldTypeArray {
			// A JSON-document array (jsonb on PostgreSQL) has no native
			// array_append; the document path applies it.
			return "", &core.ParseError{
				Kind: core.ParseErrorKindPatch, Code: ECodeOpNotSQLCompilable.String(),
				Message: fmt.Sprintf("array_append on %s (a %s field) compiles to SQL only for native array columns — apply via ApplyValidatedPatch", op.Path, op.Schema.DataType),
				Path:    op.Path,
			}
		}
	}
	if start < 0 {
		expr := col
		for _, op := range later {
			var err error
			if op.Merge {
				expr, err = mergeSQL(b.dialect, expr, op.Value, b.bind)
			} else {
				expr, err = arrayAppendSQL(b.dialect, expr, op.Value, b.bind)
			}
			if err != nil {
				return "", fmt.Errorf("%s for %s: %w", op.Op, op.Path, err)
			}
		}
		return expr, nil
	}

	base := ops[start]
	var value any
	if base.Op != PatchOpRemove {
		value = base.Value
	}
	for _, op := range later {
		if op.Merge {
			value = jsonmerge.Apply(value, op.Value)
			continue
		}
		var elems []any
		if value != nil {
			arr, ok := core.AnySlice(value)
			if !ok {
				return "", fmt.Errorf("array append for %s: base value is %T, not an array", base.Path, value)
			}
			elems = slices.Clone(arr)
		}
		value = append(elems, op.Value)
	}
	if value == nil {
		if value = base.Schema.ClearedValue(); value == nil {
			return "NULL", nil
		}
	}
	val, err := marshalIfComplex(value, base.Schema, b.dialect)
	if err != nil {
		return "", fmt.Errorf("marshaling value for %s: %w", base.Path, err)
	}
	return b.bind(val), nil
}

// mergeSQL returns the expression merging an RFC 7396 patch object into the
// JSON document expr: patch members replace, nested objects merge, nulls
// delete, and a target that is not an object merges as {} (RFC 7396 §2).
// SQLite implements RFC 7396 as json_patch; PostgreSQL has none, so the
// patch compiles level by level (pgMerge).
func mergeSQL(dialect dialect.Dialect, expr string, patch any, bind func(any) string) (string, error) {
	obj, ok := patch.(map[string]any)
	if !ok {
		return "", fmt.Errorf("merge value is %T, not an object", patch)
	}
	switch dialect.Name() {
	case "sqlite":
		doc, err := json.Marshal(obj)
		if err != nil {
			return "", err
		}
		return "json_patch(COALESCE(" + expr + ", '{}'), " + bind(string(doc)) + ")", nil
	case "postgres":
		levels := 0
		return pgMerge(expr, obj, bind, &levels)
	}
	return "", fmt.Errorf("merge patch not supported for dialect %q", dialect.Name())
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
// The wire-level value must be an array (JSON []) matching the relation's
// target column type; remove/add reject empty arrays as a no-op miss.
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
	values, err := coerceRelationValues(op.Value)
	if err != nil {
		return RelationPatchOp{}, &core.ParseError{
			Kind:    core.ParseErrorKindPatch,
			Code:    ECodeRelationValueInvalid.String(),
			Message: fmt.Sprintf("relation %q: %v", strings.TrimPrefix(op.Path, "/"), err),
			Path:    op.Path,
		}
	}
	if (verb == RelationVerbAdd || verb == RelationVerbRemove) && len(values) == 0 {
		return RelationPatchOp{}, &core.ParseError{
			Kind:    core.ParseErrorKindPatch,
			Code:    ECodeRelationValueInvalid.String(),
			Message: fmt.Sprintf("relation %q: %s requires at least one value", strings.TrimPrefix(op.Path, "/"), verb),
			Path:    op.Path,
		}
	}
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

func coerceRelationValues(value any) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	switch v := value.(type) {
	case []any:
		return v, nil
	case []string:
		out := make([]any, len(v))
		for i, s := range v {
			out[i] = s
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected JSON array, got %T", value)
	}
}

func columnName(op *PatchOperation) (string, error) {
	if op.Schema != nil && op.Schema.SQLColumn != "" {
		return op.Schema.SQLColumn, nil
	}
	// Never emit user-supplied path text as a SQL identifier — a schema without SQLColumn (and no relation) cannot be
	// compiled safely.
	return "", &core.ParseError{
		Kind: core.ParseErrorKindPatch, Code: ECodeSQLColumnMissing.String(),
		Message: fmt.Sprintf("path %s has no SQLColumn mapping; refusing to use the request path as a SQL identifier", op.Path),
		Path:    op.Path,
	}
}

// arrayAppendSQL returns the dialect-specific expression appending element
// to the array expression expr. NULL coalesces to the empty container so the
// first append to a fresh NULL column yields a one-element array. Branches on
// dialect.Name(); a dialect without a case is rejected rather than guessed.
func arrayAppendSQL(dialect dialect.Dialect, expr string, element any, bind func(any) string) (string, error) {
	switch dialect.Name() {
	case "postgres":
		// array_append preserves element order; the UPDATE's row lock
		// serialises concurrent appenders without losing values.
		// The untyped '{}' takes the column's array type, so BIGINT[] and
		// UUID[] columns append as well as TEXT[].
		return fmt.Sprintf("array_append(COALESCE(%s, '{}'), %s)", expr, bind(element)), nil
	case "sqlite":
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
		return "", fmt.Errorf("array_append not supported for dialect %q", dialect.Name())
	}
}

func marshalIfComplex(value any, schema *PathSchema, dialect SQLDialect) (any, error) {
	if schema == nil || value == nil {
		return value, nil
	}
	t := core.TypeInfo{Type: schema.DataType}
	if schema.DataType == core.FieldTypeArray && schema.GoType != nil {
		t.Elem = core.TypeOf(schema.GoType).Elem
	}
	return dialect.MarshalValue(value, t)
}
