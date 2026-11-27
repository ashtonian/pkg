package patch

import (
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
