package filter

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// elementColumn is the placeholder document column of an element path:
// each predicate on the elements rebinds it to its own element alias.
const elementColumn = "elem__"

// documentElements derives the element schema of a JSON array document
// the members of a slice-of-struct's elements tagged
// `parse:"filter"`, or the `elements` tag's paths (grammar of `paths`) for
// any JSON array. A document with element paths takes `$some` / `$none`,
// whose filters one element must satisfy together. Nil when it has none.
func documentElements(g *core.SchemaGenerator, field *core.Field, doc *FieldSchema) (*ResourceSchema, error) {
	var decls []PathDecl
	var kinds []reflect.Kind
	t := field.Type
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		elem := t.Elem()
		for elem.Kind() == reflect.Pointer {
			elem = elem.Elem()
		}
		if core.TypeOf(elem).Type == core.FieldTypeObject {
			d, k, err := structPaths(g, elem, nil, "filter")
			if err != nil {
				return nil, fmt.Errorf("elements: %w", err)
			}
			decls, kinds = d, k
		}
	}
	if tag := field.Tags.Elements; tag != "" {
		d, err := parsePathsTag(tag)
		if err != nil {
			return nil, fmt.Errorf("elements: %w", err)
		}
		for _, decl := range d {
			decls = append(decls, decl)
			kinds = append(kinds, kindOf(decl.elemOrType()))
		}
	}
	if len(decls) == 0 {
		return nil, nil
	}
	placeholder := &FieldSchema{SQLExpr: elementColumn, Storage: StorageJSONB}
	fields := make(map[string]*FieldSchema, len(decls))
	for i, d := range decls {
		segs := strings.Split(d.Path, ".")
		d.Facet = nil // elements have no facets: a facet counts rows
		f, err := newPathField(placeholder, segs, d, kinds[i])
		if err != nil {
			return nil, fmt.Errorf("element path %s: %w", d.Path, err)
		}
		f.ExternalName = d.Path
		if f.Element != nil {
			f.Element.ExternalName = d.Path
		}
		if _, dup := fields[f.ExternalName]; dup {
			return nil, fmt.Errorf("element path %s is declared twice", d.Path)
		}
		fields[f.ExternalName] = f
	}
	return &ResourceSchema{Name: elementColumn, Fields: fields}, nil
}

// elementTable is a dialect's table of an array's elements: one row per
// element, which `$some` / `$none`, an array path's NULL tests and
// `$contains`, and list facets read.
type elementTable interface {
	// elementRows is a FROM item yielding a row aliased alias per element
	// of the array f holds — a JSON array document, an array path member or
	// an array column — and none when f holds no array.
	elementRows(f *FieldSchema, alias string) string
	// objectElement is row alias's element as the document its element
	// paths read, NULL for an element that is not an object.
	objectElement(alias string) string
	// scalarElement is row alias's element as a field of f's element type
	// (f.Element), NULL for an element of another kind.
	scalarElement(f *FieldSchema, alias string) *FieldSchema
}

// elementRows reads a JSON array as jsonb_array_elements, an array path's
// member when it is one, and an array column with unnest.
func (PostgresDialect) elementRows(f *FieldSchema, alias string) string {
	var array string
	switch {
	case f.Path != nil:
		array = pgPathDocument(f.Path) + " #> " + pgPathLiteral(f.Path.Segments)
	case f.DataType == core.FieldTypeArray:
		return "unnest(" + f.SQLExpr + ") AS " + alias + "(v)"
	default:
		array = pgDocument(f)
	}
	return "jsonb_array_elements(CASE WHEN jsonb_typeof(" + array + ") = 'array' THEN " + array + " END) AS " + alias + "(v)"
}

func (PostgresDialect) objectElement(alias string) string { return alias + ".v" }

// scalarElement reads a native array column's element as it is, and a JSON
// array's as an element path (PathExpr).
func (PostgresDialect) scalarElement(f *FieldSchema, alias string) *FieldSchema {
	e := *elementOf(f)
	if f.Path == nil {
		e.SQLExpr = alias + ".v"
		return &e
	}
	e.SQLExpr = ""
	e.Path = &JSONPath{Column: alias, Storage: StorageJSONB, element: true}
	return &e
}

// elementRows reads a JSON array, an array path's member or an array
// column's JSON text, with json_each when it is an array: json_each would
// read an object's members, or a scalar as one row.
func (SQLiteDialect) elementRows(f *FieldSchema, alias string) string {
	if p := f.Path; p != nil {
		path := sqlitePathLiteral(p.Segments)
		return "json_each(CASE WHEN json_type(" + p.Column + ", " + path + ") = 'array' THEN " + p.Column + " END, " + path + ") AS " + alias
	}
	return "json_each(CASE WHEN json_type(" + f.SQLExpr + ") = 'array' THEN " + f.SQLExpr + " END) AS " + alias
}

// objectElement is an object element's JSON text; json_extract would refuse
// a scalar's text.
func (SQLiteDialect) objectElement(alias string) string {
	return "(CASE WHEN " + alias + ".type = 'object' THEN " + alias + ".value END)"
}

// scalarElement reads every element as an element path (PathExpr): an
// array column is JSON text too.
func (SQLiteDialect) scalarElement(f *FieldSchema, alias string) *FieldSchema {
	e := *elementOf(f)
	e.SQLExpr = ""
	e.Path = &JSONPath{Column: alias, Storage: StorageJSONB, element: true}
	return &e
}

// elementContainment is a dialect that answers `$some` of an element equal
// to a value, or to one of a set, by containment, which an index on the
// document serves.
type elementContainment interface {
	containsElement(f *FieldSchema, cond *FilterNode, bind func(any) string) (sql string, ok bool, err error)
}

// containsElement renders `$some` of a string, boolean or number element of
// a jsonb array path equal to a value (`$eq`) as `doc @> fragment`, and to
// one of at most ContainmentMaxValues values (`$in`) as `doc @> ANY(...)`,
// each fragment the path's objects down to a one-element array. Containment
// holds exactly when the member is an array with an equal element; the
// document's NULL test keeps a NULL document False rather than Unknown, so
// `$some` stays two-valued under any negation. Decimals keep the element
// table (a decimal element may be held as a string), as does a path through
// a segment `#>` would read as an array index.
func (d PostgresDialect) containsElement(f *FieldSchema, cond *FilterNode, bind func(any) string) (string, bool, error) {
	p := f.Path
	if p == nil || p.Storage != StorageJSONB || !containable(f.Elem) || slices.ContainsFunc(p.Segments, isArrayIndex) ||
		cond == nil || cond.Field == nil || !isElement(cond.Field) {
		return "", false, nil
	}
	fragment := func(v any) (string, error) { return containmentFragment(p.Segments, []any{v}) }
	switch cond.Op {
	case OpEq:
		if cond.Value == nil {
			return "", false, nil // `= NULL` holds for no element; [null] would contain it
		}
		frag, err := fragment(cond.Value)
		if err != nil {
			return "", false, fmt.Errorf("field %s: %w", f.ExternalName, err)
		}
		return "(" + p.Column + " IS NOT NULL AND " + p.Column + " @> " + bind(frag) + "::jsonb)", true, nil
	case OpIn:
		members, ok := core.AnySlice(cond.Value)
		if !ok || len(members) == 0 || len(members) > d.containmentMaxValues() || slices.Contains(members, nil) {
			return "", false, nil
		}
		frags, err := typedSlice(members, fragment)
		if err != nil {
			return "", false, fmt.Errorf("field %s: %w", f.ExternalName, err)
		}
		return "(" + p.Column + " IS NOT NULL AND " + p.Column + " @> ANY(" + bind(frags) + "::jsonb[]))", true, nil
	}
	return "", false, nil
}

// buildElements compiles `$some` / `$none` to one EXISTS / NOT EXISTS over
// the array's elements (elementTable), the node's child holding for one
// element: a filter on a JSON array document's element paths, or a
// condition on a scalar array's element. A scalar array's elements are its
// values of its element type, so an element of another kind is no element.
func (b *SQLBuilder) buildElements(s *buildState, node *FilterNode) (string, error) {
	f := node.Field
	table, ok := b.dialect.(elementTable)
	if !ok {
		return "", fmt.Errorf("field %s: dialect %s reads no array elements for $%s", f.ExternalName, b.dialect.Name(), node.Op)
	}
	negate := ""
	if node.Op == OpNone {
		negate = "NOT "
	}
	var child *FilterNode
	if len(node.Children) > 0 {
		child = node.Children[0]
	}
	if c, ok := b.dialect.(elementContainment); ok && isScalarArray(f) {
		sql, ok, err := c.containsElement(f, child, s.binder(b.dialect))
		if err != nil || ok {
			return negate + sql, err
		}
	}
	alias := "elem" + strconv.Itoa(b.depth+1) + "__"
	var conds []string
	if isScalarArray(f) {
		elem := table.scalarElement(f, alias)
		conds = append(conds, FieldExpr(b.dialect, elem)+" IS NOT NULL")
		if child != nil {
			child = bindElement(child, elem)
		}
	} else if child != nil {
		var err error
		if child, err = rebaseElements(child, table.objectElement(alias), map[*FieldSchema]*FieldSchema{}); err != nil {
			return "", err
		}
	}
	if child != nil {
		nb := &SQLBuilder{schema: b.schema, dialect: b.dialect, scope: b.scope, related: b.related, depth: b.depth + 1, visible: b.visible}
		cond, err := s.selecting(func() (string, error) { return nb.buildNode(s, child) })
		if err != nil {
			return "", err
		}
		conds = append(conds, cond)
	}
	sql := negate + "EXISTS (SELECT 1 FROM " + table.elementRows(f, alias)
	if len(conds) > 0 {
		sql += " WHERE " + strings.Join(conds, " AND ")
	}
	return sql + ")", nil
}

// buildArrayPath compiles a NULL test or `$contains` on an array path,
// whose value is its elements of its element type (PathValue): NULL when it
// has none, so `$contains` is Unknown there, as on a NULL array column.
func (b *SQLBuilder) buildArrayPath(s *buildState, node *FilterNode) (string, error) {
	f := node.Field
	switch node.Op {
	case OpNotNull:
		return b.buildElements(s, &FilterNode{Field: f, Op: OpSome})
	case OpNull:
		return b.buildElements(s, &FilterNode{Field: f, Op: OpNone})
	case OpContains:
		values, ok := core.AnySlice(node.Value)
		if !ok {
			return "", fmt.Errorf("field %s: $contains on an array requires an array value, got %T", f.ExternalName, node.Value)
		}
		present, err := b.buildElements(s, &FilterNode{Field: f, Op: OpSome})
		if err != nil {
			return "", err
		}
		all := make([]string, 0, len(values))
		for _, v := range values {
			has, err := b.buildElements(s, &FilterNode{Field: f, Op: OpSome, Children: []*FilterNode{{Field: elementOf(f), Op: OpEq, Value: v}}})
			if err != nil {
				return "", err
			}
			all = append(all, has)
		}
		held := "1=1"
		if len(all) > 0 {
			held = strings.Join(all, " AND ")
		}
		return "(CASE WHEN " + present + " THEN (" + held + ") END)", nil
	}
	return "", unsupportedOperator(f, node.Op)
}

// bindElement is n with a scalar array's element field replaced by elem,
// the element of one row of its element table.
func bindElement(n *FilterNode, elem *FieldSchema) *FilterNode {
	out := *n
	if n.Field != nil && isElement(n.Field) {
		out.Field = elem
	}
	if len(n.Children) > 0 {
		out.Children = make([]*FilterNode, len(n.Children))
		for i, c := range n.Children {
			out.Children[i] = bindElement(c, elem)
		}
	}
	return &out
}

// rebaseElements is n with its element paths reading value, the element. A
// scalar array's element field inside is bound by its own `$some` /
// `$none`.
func rebaseElements(n *FilterNode, value string, moved map[*FieldSchema]*FieldSchema) (*FilterNode, error) {
	out := *n
	if f := n.Field; f != nil && !isElement(f) {
		if f.Path == nil || f.Path.Column != elementColumn {
			return nil, fmt.Errorf("field %s is not an element path", f.ExternalName)
		}
		m, ok := moved[f]
		if !ok {
			c := *f
			p := *f.Path
			p.Column, p.Storage = value, StorageJSONB
			c.Path = &p
			m = &c
			moved[f] = m
		}
		out.Field = m
	}
	if len(n.Children) > 0 {
		out.Children = make([]*FilterNode, len(n.Children))
		for i, c := range n.Children {
			rc, err := rebaseElements(c, value, moved)
			if err != nil {
				return nil, err
			}
			out.Children[i] = rc
		}
	}
	return &out, nil
}

// DocumentElements is a JSON array document's elements, decoded as
// PathValue decodes documents; nil for a document that is not an array. A
// backend evaluating `$some` / `$none` in memory tests the node's child on
// each element, reading element paths with PathValue(element, field).
func DocumentElements(doc any) ([]any, error) {
	root, err := decodeDocument(doc)
	if err != nil {
		return nil, err
	}
	elems, _ := root.([]any)
	return elems, nil
}
