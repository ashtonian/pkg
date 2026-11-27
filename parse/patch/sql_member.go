package patch

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/ashtonian/pkg/parse/core"
)

// A Precondition is what one JSON Patch operation on a member of a JSON
// document column needs of the document as stored for RFC 6902 to apply it:
// the member itself, for a replace or a remove, or the object it is added
// to, for an add. BuildPatchSQL settles what the patch's earlier operations
// decide and leaves the rest to the stored row.
type Precondition struct {
	// Op is the operation's index in the patch, and Path its path.
	Op   int
	Path string
	// Column holds the document, and Member is the pointer's tokens below
	// the field the column stores.
	Column string
	Member []string
	// Exists is set when the member must exist; otherwise the object it is
	// added to must.
	Exists bool
}

// RenderPreconditions renders pre for d. all is true of a row exactly when
// every precondition holds of its stored documents; firstUnmet is the Op of
// the first that does not, or NULL when all hold. Both use the same
// placeholders, which bind binds and returns. A precondition naming no
// member is an error: BuildPatchSQL makes none.
func RenderPreconditions(d SQLDialect, pre []Precondition, bind func(any) string) (all, firstUnmet string, err error) {
	if len(pre) == 0 {
		return "", "", nil
	}
	conds := make([]string, len(pre))
	var unmet strings.Builder
	unmet.WriteString("CASE")
	for i, p := range pre {
		if len(p.Member) == 0 {
			return "", "", fmt.Errorf("patch: precondition of operation %d (%s) names no member of column %s", p.Op, p.Path, p.Column)
		}
		cond, err := p.condition(d, bind)
		if err != nil {
			return "", "", dialectError("precondition", p.Path, err)
		}
		conds[i] = cond
		fmt.Fprintf(&unmet, " WHEN NOT %s THEN %d", cond, p.Op)
	}
	unmet.WriteString(" END")
	return "(" + strings.Join(conds, " AND ") + ")", unmet.String(), nil
}

// condition is p as a condition on the stored row that is never NULL. A
// member is read by object keys only: PostgreSQL's #> would index an array
// with a token that reads as an integer, which documentPaths refuses where
// an array can stand, and SQLite's quoted labels never index one.
func (p Precondition) condition(d SQLDialect, bind func(any) string) (string, error) {
	doc, err := d.MemberDocument(d.QuoteIdent(p.Column))
	if err != nil {
		return "", err
	}
	if p.Exists {
		return d.HasMember(doc, p.Member, bind)
	}
	return d.IsObject(doc, p.Member[:len(p.Member)-1], bind)
}

// memberTokens are the tokens of op's path below the field its column
// stores; none for an operation on the whole field.
func memberTokens(op *PatchOperation) []string {
	tokens, err := core.ParsePointer(op.Path)
	if err != nil || len(tokens) < 2 {
		return nil
	}
	return tokens[1:]
}

// indexLike matches a token PostgreSQL reads as an array index, which
// RFC 6901 reads as one too where it is a plain integer, and the "-" that
// names the end of an array.
var indexLike = regexp.MustCompile(`^\s*[+-]?[0-9]+\s*$|^-$`)

// checkMemberPath refuses an operation on a member that SQL could not tell
// from an array element: a member of an array, which RFC 6902 inserts into
// rather than sets, and a token that reads as an index under a parent whose
// declared type does not make it an object. A member of a field schema
// declares neither is judged by its tokens alone. It also refuses a member
// below a type that decodes itself, whose document only its decoder judges;
// the parser holds a member of any other declared type to it
// (checkDocumentMember).
func checkMemberPath(schema *PatchSchema, op *PatchOperation) error {
	tokens, err := core.ParsePointer(op.Path)
	if err != nil {
		return err
	}
	var t reflect.Type
	field, ok := schema.pathSet().resolve(core.FormatPointer(tokens[0]))
	if ok {
		if !documentType(field.DataType) {
			return notCompilable(op, fmt.Sprintf("%s is a member of a %s field, not of a JSON document", op.Path, field.DataType))
		}
		t = field.GoType
	}
	for _, token := range tokens[1:] {
		declared := derefType(t)
		if declared != nil && core.CustomJSON(declared) {
			return notCompilable(op, fmt.Sprintf("%s is a member of %s, which decodes itself, so SQL cannot hold the stored document to it", op.Path, gowalker.TypeText(declared)))
		}
		switch shapeOf(t) {
		case shapeArray:
			return notCompilable(op, fmt.Sprintf("%s writes an array element, which compiles to SQL only as the whole array", op.Path))
		case shapeUnknown:
			if indexLike.MatchString(token) {
				return notCompilable(op, fmt.Sprintf("%s names member %q, which reads as an array index where the document's type does not make its parent an object", op.Path, token))
			}
		}
		t = memberType(t, token)
	}
	return nil
}

func derefType(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func documentType(t core.FieldType) bool {
	return t == "" || t == core.FieldTypeJSON || t == core.FieldTypeObject || t == core.FieldTypeAny
}

func notCompilable(op *PatchOperation, why string) error {
	err := core.NewError(ECodeOpNotSQLCompilable, why+" — apply via ApplyValidatedPatch", core.WithPath(op.Path))
	return err
}

type memberShape uint8

const (
	shapeUnknown memberShape = iota
	shapeObject
	shapeArray
)

// shapeOf is what encoding/json writes for t: an object for a struct or a
// map, an array for a slice other than bytes or an array, and unknown for
// anything that encodes itself or is not declared.
func shapeOf(t reflect.Type) memberShape {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || untypedDocument(t) || core.CustomJSON(t) {
		return shapeUnknown
	}
	switch t.Kind() {
	case reflect.Struct, reflect.Map:
		return shapeObject
	case reflect.Slice, reflect.Array:
		if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
			return shapeUnknown
		}
		return shapeArray
	}
	return shapeUnknown
}

// memberType is the type of member name of t, or nil when t declares none.
func memberType(t reflect.Type, name string) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || shapeOf(t) != shapeObject {
		return nil
	}
	if t.Kind() == reflect.Map {
		return t.Elem()
	}
	f := jsonMember(t, name)
	if f != nil {
		return f.Type()
	}
	return nil
}

// documentFold folds the operations of one JSON document column when some
// of them write a member. Until an operation writes the whole field, the
// document is the stored one: expr is SQL over the column, and facts hold
// what the earlier member operations settled, so an operation's
// precondition is decided from them or left to the stored row. Once one
// writes the whole field, the document is known: the rest apply to it in
// Go, and a precondition they fail fails the patch for every row.
type documentFold struct {
	b   *setBuilder
	col string

	known bool
	// doc is the known document as encoding/json decodes it, nil for a
	// removed field; base is the operation that wrote the field whole.
	doc  any
	base *PatchOperation
	// folded marks member operations applied to doc, which then binds in
	// place of base's value.
	folded bool

	expr  string
	facts map[string]memberFact
	// overwritten marks an operation a later whole-field write replaces:
	// RFC 6902 still requires its precondition, but its SQL would bind
	// arguments the statement never reads.
	overwritten bool
}

// A memberFact is what earlier operations made of one member of the stored
// document: absent, or a value.
type memberFact struct {
	absent bool
	value  any
}

// documentExpr folds the operations of the document column col, some of
// which write a member. When an operation writes the document whole and
// the members fold onto its value in Go, the column is written whole, as
// that operation's path declares it; otherwise its members are written
// over the stored document.
func (b *setBuilder) documentExpr(col string, ops []*PatchOperation) (columnWrite, error) {
	doc, err := b.dialect.MemberDocument(b.dialect.QuoteIdent(col))
	if err != nil {
		return columnWrite{}, dialectError("document", fieldPath(ops[0]), err)
	}
	f := &documentFold{b: b, col: col, expr: doc, facts: map[string]memberFact{}}
	whole := -1
	for i, op := range ops {
		if len(memberTokens(op)) == 0 {
			whole = i
		}
	}
	for i, op := range ops {
		f.overwritten = i < whole
		err := f.apply(op)
		if err != nil {
			return columnWrite{}, err
		}
	}
	if !f.known {
		members := columnWrite{expr: f.expr, members: true}
		return members, nil
	}
	value := f.doc
	if !f.folded {
		value, _ = wholeValue(f.base)
	}
	if value == nil {
		cleared := columnWrite{expr: "NULL", schema: f.base.Schema}
		return cleared, nil
	}
	val, err := marshalIfComplex(value, f.base.Schema, f.b.dialect)
	if err != nil {
		return columnWrite{}, fmt.Errorf("marshaling value for %s: %w", f.base.Path, err)
	}
	bound := columnWrite{expr: f.b.bind(val), schema: f.base.Schema}
	return bound, nil
}

func (f *documentFold) apply(op *PatchOperation) error {
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
		f.known, f.doc, f.base, f.folded = true, doc, op, false
		clear(f.facts)
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
	if f.known {
		doc, ok := applyMember(f.doc, op.Op, member, value)
		if !ok {
			return f.unmet(op, member)
		}
		f.doc, f.folded = doc, true
		return nil
	}
	err := f.require(op, member)
	if err != nil {
		return err
	}
	if !f.overwritten {
		err := f.compile(op.Op, member, value)
		if err != nil {
			return dialectError(string(op.Op), op.Path, err)
		}
	}
	f.record(op.Op, member, value)
	return nil
}

// require decides op's precondition from the facts, or leaves it to the
// stored row.
func (f *documentFold) require(op *PatchOperation, member []string) error {
	need := Precondition{Op: f.b.index[op], Path: op.Path, Column: f.col, Member: member, Exists: op.Op != PatchOpAdd}
	target := member
	if !need.Exists {
		target = member[:len(member)-1]
	}
	if len(target) > 0 {
		switch state, v := f.lookup(target); state {
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
	f.b.preconditions = append(f.b.preconditions, need)
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
func (f *documentFold) lookup(member []string) (memberState, any) {
	for i := len(member); i > 0; i-- {
		fact, ok := f.facts[core.FormatPointer(member[:i]...)]
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
func (f *documentFold) record(op PatchOp, member []string, value any) {
	for i := len(member) - 1; i > 0; i-- {
		key := core.FormatPointer(member[:i]...)
		fact, ok := f.facts[key]
		if ok {
			fact.value, _ = applyMember(fact.value, op, member[i:], value)
			f.facts[key] = fact
			return
		}
	}
	key := core.FormatPointer(member...)
	for k := range f.facts {
		if strings.HasPrefix(k, key+"/") {
			delete(f.facts, k)
		}
	}
	f.facts[key] = memberFact{absent: op == PatchOpRemove, value: value}
}

// compile applies op to the stored document's expression
// (SQLDialect.SetMember, RemoveMember). A set writes an object's member
// whether or not it exists, which the preconditions make an add's or a
// replace's.
func (f *documentFold) compile(op PatchOp, member []string, value any) error {
	if op == PatchOpRemove {
		expr, err := f.b.dialect.RemoveMember(f.expr, member, f.b.bind)
		if err != nil {
			return err
		}
		f.expr = expr
		return nil
	}
	doc, err := json.Marshal(value)
	if err != nil {
		return err
	}
	expr, err := f.b.dialect.SetMember(f.expr, member, doc, f.b.bind)
	if err != nil {
		return err
	}
	f.expr = expr
	return nil
}

func (f *documentFold) unmet(op *PatchOperation, member []string) error {
	return Precondition{Op: f.b.index[op], Path: op.Path, Column: f.col, Member: member, Exists: op.Op != PatchOpAdd}.Err()
}

// Err is the error of an operation whose precondition the document does not
// meet, as the document path reports it: E2077, naming the operation.
func (p Precondition) Err() error {
	msg := fmt.Sprintf("operation %d: %s does not exist", p.Op, p.Path)
	if !p.Exists {
		msg = fmt.Sprintf("operation %d: %s has no object to be added to", p.Op, p.Path)
	}
	err := core.NewError(ECodePatchValueInvalid, msg, core.WithPath(p.Path), core.WithIndex(p.Op))
	return err
}

// applyMember applies op to member of doc, a document as encoding/json
// decodes one, by RFC 6902: an add sets a member of an existing object, a
// replace and a remove need the member. It reports false, leaving doc,
// where the operation does not apply.
func applyMember(doc any, op PatchOp, member []string, value any) (any, bool) {
	parent, ok := documentMember(doc, member[:len(member)-1])
	if !ok {
		return doc, false
	}
	obj, ok := parent.(map[string]any)
	if !ok {
		return doc, false
	}
	key := member[len(member)-1]
	_, exists := obj[key]
	if !exists && op != PatchOpAdd {
		return doc, false
	}
	if op == PatchOpRemove {
		delete(obj, key)
	} else {
		obj[key] = value
	}
	return doc, true
}

// documentMember is the member of v that tokens name, through objects only.
func documentMember(v any, tokens []string) (any, bool) {
	for _, token := range tokens {
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok = obj[token]
		if !ok {
			return nil, false
		}
	}
	return v, true
}
