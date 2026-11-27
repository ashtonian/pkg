package patch

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
)

// ComposedPatch is a parsed patch composed into the update it makes of a
// row, decided once for every store that applies it: the columns it
// assigns and what each becomes, the server's stamp and counters, the RFC
// 6902 preconditions the stored row decides, and the relation operations.
// BuildPatchSQL renders one for a SQL dialect (SQL), and a store that holds
// rows itself applies one to a row's stored values (Apply), so both make
// the same decisions and refuse the same patches. It is immutable and safe
// for concurrent use.
type ComposedPatch struct {
	// columns are the patch's columns, in first-touched order.
	columns []*columnChange
	// stamp is the stamped column, "" for none: at, or the SQL expression
	// stampSQL when it is set.
	stamp    string
	stampAt  time.Time
	stampSQL string
	// increments are the counted columns, in the order they were named.
	increments    []string
	preconditions []Precondition
	relations     []RelationPatchOp
}

// ComposePatch composes p for any store, with the options that decide what
// the update writes beyond the patch: WithAutoTimestamp,
// WithAutoTimestampSQL, WithIncrement and WithTimeFunc. The rendering
// options, WithStartParam and WithChangePredicate, are SQL's: pass them to
// ComposedPatch.SQL. It refuses what BuildPatchSQL refuses, with the same
// errors: test (E2068), move and copy, an array element, a member whose
// name reads as an array index where the document's type does not make its
// parent an object, array_append below a field or on a document (E2070), a
// member operation the patch's own operations make fail (E2077), a relation
// verb a relation does not take, and a patch no parser produced
// (ErrUnparsed).
func ComposePatch(p *ParsedPatch, opts ...PatchSQLOption) (*ComposedPatch, error) {
	cfg := newPatchSQLConfig(opts)
	if cfg.startParam != 0 || cfg.changePredicate {
		return nil, errors.New("patch: ComposePatch takes no rendering option; pass WithStartParam and WithChangePredicate to ComposedPatch.SQL")
	}
	return compose(p, cfg)
}

// Columns are the columns the update assigns, in the order BuildPatchSQL's
// SET clause assigns them: the patch's, first-touched, then the stamp, then
// the increments. None when the patch assigns no column of its own (an
// empty patch, or one of relation operations or server columns alone),
// which stamps and counts nothing either.
func (c *ComposedPatch) Columns() []string {
	if len(c.columns) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.columns)+1+len(c.increments))
	for _, ch := range c.columns {
		out = append(out, ch.column)
	}
	if c.stamp != "" {
		out = append(out, c.stamp)
	}
	return append(out, c.increments...)
}

// Assigns reports whether the update assigns a column (Columns).
func (c *ComposedPatch) Assigns() bool { return len(c.columns) > 0 }

// Preconditions are what the stored row must hold for the patch's member
// operations to apply, in patch order: a row that fails one is one the
// patch does not apply to, and the first it fails names the operation
// (Precondition.Err).
func (c *ComposedPatch) Preconditions() []Precondition { return slices.Clone(c.preconditions) }

// RelationOps are the patch's relation membership changes, in patch order.
func (c *ComposedPatch) RelationOps() []RelationPatchOp { return slices.Clone(c.relations) }

// Empty reports an update that writes nothing: it assigns no column and
// changes no relation.
func (c *ComposedPatch) Empty() bool { return len(c.columns) == 0 && len(c.relations) == 0 }

// changeKind is how a column's operations fold.
type changeKind uint8

const (
	// changeSet writes the column whole: a value, or NULL.
	changeSet changeKind = iota
	// changeMerge merges an RFC 7396 object into the stored document.
	changeMerge
	// changeAppend appends elements to the stored array.
	changeAppend
	// changeMembers adds, replaces and removes members of the stored
	// document.
	changeMembers
)

// columnChange is what one column's operations fold to.
type columnChange struct {
	column string
	kind   changeKind
	// ops are the column's operations in patch order; the first names the
	// field a failure to render reports (fieldPath).
	ops []*PatchOperation
	// schema is the path schema a column written whole is compared as
	// (SQLDialect.ChangedCondition); members are compared as a document.
	schema *PathSchema
	// value is what changeSet writes, nil for NULL, bound as base's path
	// declares it; stored is its storage form, computed once.
	value  any
	base   *PatchOperation
	stored func() (any, error)
	// merge is changeMerge's object, and mergeDocument its decoded JSON
	// document, computed once.
	merge         map[string]any
	mergeDocument func() (any, error)
	// appends are changeAppend's operations, each appending its value.
	appends []*PatchOperation
	// edits are changeMembers' member writes, in patch order.
	edits []memberEdit
}

// memberEdit is one add, replace or remove of a member of a stored
// document: value is the JSON text an add or a replace sets it to, and
// need the precondition the operation has of the document it applies to.
type memberEdit struct {
	op     *PatchOperation
	member []string
	value  json.RawMessage
	need   Precondition
}

// compose is ComposePatch under cfg.
func compose(p *ParsedPatch, cfg *patchSQLConfig) (*ComposedPatch, error) {
	server, err := serverColumns(cfg)
	if err != nil {
		return nil, err
	}
	err = checkParsed(p)
	if err != nil {
		return nil, err
	}
	c := &ComposedPatch{}
	if len(p.Operations) == 0 {
		return c, nil
	}
	var (
		columns   []string // first-touched order
		columnOps = map[string][]*PatchOperation{}
		index     = make(map[*PatchOperation]int, len(p.Operations))
	)
	for i, op := range p.Operations {
		index[op] = i
		if op.Schema != nil && op.Schema.Relation != nil {
			rop, err := buildRelationPatchOp(op)
			if err != nil {
				return nil, err
			}
			c.relations = append(c.relations, rop)
			continue
		}
		err := checkSQLCompilable(op)
		if err != nil {
			return nil, err
		}
		if len(memberTokens(op)) > 0 {
			err := checkMemberPath(p.schema, op)
			if err != nil {
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
		_, seen := columnOps[col]
		if !seen {
			columns = append(columns, col)
		}
		columnOps[col] = append(columnOps[col], op)
	}
	f := &folder{index: index}
	for _, col := range columns {
		ch, err := f.column(col, columnOps[col])
		if err != nil {
			return nil, err
		}
		c.columns = append(c.columns, ch)
	}
	// Columns fold one after another, so the preconditions are put back in
	// patch order: the first one a row fails is the operation that fails.
	slices.SortFunc(f.preconditions, func(x, y Precondition) int { return cmp.Compare(x.Op, y.Op) })
	c.preconditions = f.preconditions
	if len(c.columns) > 0 {
		c.stamp, c.stampSQL = cfg.autoTimestamp, cfg.autoTimestampSQL
		if c.stamp != "" && c.stampSQL == "" {
			c.stampAt = cfg.now()
		}
		c.increments = slices.Clone(cfg.increments)
	}
	return c, nil
}

// folder folds each column's operations: index is each operation's index
// in the patch, and preconditions are the member operations' the stored
// rows decide.
type folder struct {
	index         map[*PatchOperation]int
	preconditions []Precondition
}

// column folds one column's ordered operations into one change. A merge
// patch writes a column with one operation, the only kind that merges, so a
// merge applies to the stored column. Operations on members of a document
// fold in document. Other JSON Patch operations fold: the last whole-value
// operation (replace/add/remove) sets the base and earlier ones are
// overwritten, and appends after it fold onto its value, so one value is
// written; appends with no base apply to the stored column.
func (f *folder) column(col string, ops []*PatchOperation) (*columnChange, error) {
	last := ops[len(ops)-1].Schema
	if slices.ContainsFunc(ops, func(op *PatchOperation) bool { return op.Merge }) {
		if len(ops) != 1 {
			return nil, fmt.Errorf("patch: column %s: a merge shares its column with another operation, which no parser produces", col)
		}
		obj, ok := ops[0].Value.(map[string]any)
		if !ok {
			err := fmt.Errorf("merge value is %s, not an object", gowalker.TypeText(reflect.TypeOf(ops[0].Value)))
			return nil, dialectError(string(ops[0].Op), ops[0].Path, err)
		}
		merged := &columnChange{column: col, kind: changeMerge, ops: ops, schema: last, merge: obj}
		merged.mergeDocument = sync.OnceValues(func() (any, error) { return documentOf(obj) })
		return merged, nil
	}
	if slices.ContainsFunc(ops, func(op *PatchOperation) bool { return len(memberTokens(op)) > 0 }) {
		return f.document(col, ops)
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
			notNative := core.NewError(ECodeOpNotSQLCompilable, fmt.Sprintf("array_append on %s (a %s field) compiles to SQL only for native array columns — apply via ApplyValidatedPatch", op.Path, op.Schema.DataType),
				core.WithPath(op.Path))
			return nil, notNative
		}
	}
	if start < 0 {
		appended := &columnChange{column: col, kind: changeAppend, ops: ops, schema: last, appends: later}
		return appended, nil
	}
	base := ops[start]
	value, _ := wholeValue(base)
	for _, op := range later {
		var elems []any
		if value != nil {
			arr, ok := core.AnySlice(value)
			if !ok {
				return nil, fmt.Errorf("array append for %s: base value is %s, not an array", base.Path, gowalker.TypeText(reflect.TypeOf(value)))
			}
			elems = slices.Clone(arr)
		}
		elems = append(elems, op.Value)
		value = elems
	}
	return setChange(col, ops, last, base, value), nil
}

// setChange is the change writing col whole as value, nil for NULL, bound
// as base's path declares it and compared as schema.
func setChange(col string, ops []*PatchOperation, schema *PathSchema, base *PatchOperation, value any) *columnChange {
	ch := &columnChange{column: col, kind: changeSet, ops: ops, schema: schema}
	if value == nil {
		return ch
	}
	ch.value, ch.base = value, base
	t := pathType(base.Schema)
	ch.stored = sync.OnceValues(func() (any, error) { return core.StorageValue(t, value) })
	return ch
}

// pathType is the column type a path schema declares: its field type and
// storage, and a typed array's element and a number's Go kind.
func pathType(schema *PathSchema) core.TypeInfo {
	t := core.TypeInfo{Type: schema.DataType, SQLType: schema.SQLType}
	if schema.GoType == nil {
		return t
	}
	declared := core.TypeOf(schema.GoType)
	if schema.DataType == core.FieldTypeArray {
		t.Elem = declared.Elem
	}
	t.NumberKind = declared.NumberKind
	return t
}

// documentFold folds the operations of one JSON document column when some
// of them write a member. Until an operation writes the whole field, the
// document is the stored one: edits are applied to it, and facts hold what
// the earlier member operations settled, so an operation's precondition is
// decided from them or left to the stored row. Once one writes the whole
// field, the document is known: the rest apply to it here, and a
// precondition they fail fails the patch for every row.
type documentFold struct {
	f   *folder
	col string

	known bool
	// doc is the known document as encoding/json decodes it, nil for a
	// removed field; base is the operation that wrote the field whole.
	doc  any
	base *PatchOperation
	// folded marks member operations applied to doc, which is then written
	// in place of base's value.
	folded bool

	edits []memberEdit
	facts map[string]memberFact
	// overwritten marks an operation a later whole-field write replaces:
	// RFC 6902 still requires its precondition, but it writes nothing.
	overwritten bool
}

// A memberFact is what earlier operations made of one member of the stored
// document: absent, or a value.
type memberFact struct {
	absent bool
	value  any
}

// document folds the operations of the document column col, some of which
// write a member. When an operation writes the document whole and the
// members fold onto its value, the column is written whole, as that
// operation's path declares it; otherwise its members are written over the
// stored document.
func (f *folder) document(col string, ops []*PatchOperation) (*columnChange, error) {
	d := &documentFold{f: f, col: col, facts: map[string]memberFact{}}
	whole := -1
	for i, op := range ops {
		if len(memberTokens(op)) == 0 {
			whole = i
		}
	}
	for i, op := range ops {
		d.overwritten = i < whole
		err := d.apply(op)
		if err != nil {
			return nil, err
		}
	}
	if !d.known {
		members := &columnChange{column: col, kind: changeMembers, ops: ops, edits: d.edits}
		return members, nil
	}
	value := d.doc
	if !d.folded {
		value, _ = wholeValue(d.base)
	}
	return setChange(col, ops, d.base.Schema, d.base, value), nil
}

func (d *documentFold) apply(op *PatchOperation) error {
	member := memberTokens(op)
	if len(member) == 0 {
		if op.Op != PatchOpReplace && op.Op != PatchOpAdd && op.Op != PatchOpRemove {
			return notCompilable(op, fmt.Sprintf("%s on %s compiles to SQL only for a native array column", op.Op, op.Path))
		}
		// Members written next start from what the write leaves: a
		// cleared map is an empty object they are added to.
		value, _ := wholeValue(op)
		doc, err := documentOf(value)
		if err != nil {
			return fmt.Errorf("%s for %s: %w", op.Op, op.Path, err)
		}
		d.known, d.doc, d.base, d.folded = true, doc, op, false
		clear(d.facts)
		return nil
	}
	if op.Op != PatchOpAdd && op.Op != PatchOpReplace && op.Op != PatchOpRemove {
		return notCompilable(op, fmt.Sprintf("%s on member %s does not compile to SQL", op.Op, op.Path))
	}
	var value any
	if op.Op != PatchOpRemove {
		var err error
		value, err = documentOf(op.Value)
		if err != nil {
			return fmt.Errorf("%s for %s: %w", op.Op, op.Path, err)
		}
	}
	if d.known {
		doc, ok := applyMember(d.doc, op.Op, member, value)
		if !ok {
			return d.need(op, member).Err()
		}
		d.doc, d.folded = doc, true
		return nil
	}
	err := d.require(op, member)
	if err != nil {
		return err
	}
	if !d.overwritten {
		err := d.edit(op, member, value)
		if err != nil {
			return dialectError(string(op.Op), op.Path, err)
		}
	}
	d.record(op.Op, member, value)
	return nil
}

// need is the precondition op, a member operation, has of the document.
func (d *documentFold) need(op *PatchOperation, member []string) Precondition {
	need := Precondition{Op: d.f.index[op], Path: op.Path, Column: d.col, Member: member, Exists: op.Op != PatchOpAdd}
	return need
}

// require decides op's precondition from the facts, or leaves it to the
// stored row.
func (d *documentFold) require(op *PatchOperation, member []string) error {
	need := d.need(op, member)
	target := member
	if !need.Exists {
		target = member[:len(member)-1]
	}
	if len(target) > 0 {
		switch state, v := d.lookup(target); state {
		case memberAbsent:
			return need.Err()
		case memberPresent:
			_, object := v.(map[string]any)
			if need.Exists || object {
				return nil
			}
			return need.Err()
		}
	}
	d.f.preconditions = append(d.f.preconditions, need)
	return nil
}

type memberState uint8

const (
	memberUnknown memberState = iota
	memberAbsent
	memberPresent
)

// lookup is what the facts settle about member: absent, present with its
// value, or unknown, which only the stored row tells. Earlier operations on
// other members leave it as stored.
func (d *documentFold) lookup(member []string) (memberState, any) {
	for i := len(member); i > 0; i-- {
		fact, ok := d.facts[core.FormatPointer(member[:i]...)]
		if !ok {
			continue
		}
		if fact.absent {
			return memberAbsent, nil
		}
		v, found := documentMember(fact.value, member[i:])
		if found {
			return memberPresent, v
		}
		return memberAbsent, nil
	}
	return memberUnknown, nil
}

// record keeps what op made of member: in the value of the fact on an
// ancestor when there is one, else as the member's own fact, replacing any
// on its descendants.
func (d *documentFold) record(op PatchOp, member []string, value any) {
	for i := len(member) - 1; i > 0; i-- {
		key := core.FormatPointer(member[:i]...)
		fact, ok := d.facts[key]
		if ok {
			fact.value, _ = applyMember(fact.value, op, member[i:], value)
			d.facts[key] = fact
			return
		}
	}
	key := core.FormatPointer(member...)
	for k := range d.facts {
		if strings.HasPrefix(k, key+"/") {
			delete(d.facts, k)
		}
	}
	d.facts[key] = memberFact{absent: op == PatchOpRemove, value: value}
}

// edit writes op over the stored document: a set writes an object's member
// whether or not it exists, which the preconditions make an add's or a
// replace's.
func (d *documentFold) edit(op *PatchOperation, member []string, value any) error {
	e := memberEdit{op: op, member: member, need: d.need(op, member)}
	if op.Op != PatchOpRemove {
		text, err := json.Marshal(value)
		if err != nil {
			return err
		}
		e.value = text
	}
	d.edits = append(d.edits, e)
	return nil
}
