package filter

import (
	"fmt"
	"reflect"
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
			kinds = append(kinds, kindOf(decl.Type))
		}
	}
	if len(decls) == 0 {
		return nil, nil
	}
	placeholder := &FieldSchema{SQLExpr: elementColumn, Storage: StorageJSONB}
	fields := make(map[string]*FieldSchema, len(decls))
	for i, d := range decls {
		segs := strings.Split(d.Path, ".")
		f, err := newPathField(placeholder, segs, d.Type, kinds[i], d.Ops)
		if err != nil {
			return nil, fmt.Errorf("element path %s: %w", d.Path, err)
		}
		f.ExternalName = d.Path
		f.Label = d.Label
		if _, dup := fields[f.ExternalName]; dup {
			return nil, fmt.Errorf("element path %s is declared twice", d.Path)
		}
		fields[f.ExternalName] = f
	}
	return &ResourceSchema{Name: elementColumn, Fields: fields}, nil
}

// buildElements compiles `$some` / `$none` on a JSON array document to one
// EXISTS / NOT EXISTS over its elements, the node's child — a filter on the
// element paths — holding for one element. A document that is not an array
// has no elements.
func (b *SQLBuilder) buildElements(s *buildState, node *FilterNode) (string, error) {
	f := node.Field
	alias := "elem" + strconv.Itoa(b.depth+1) + "__"
	var from, value string
	if b.dialect.Name() == (PostgresDialect{}).Name() {
		doc := pgDocument(f)
		from = "jsonb_array_elements(CASE WHEN jsonb_typeof(" + doc + ") = 'array' THEN " + doc + " END) AS " + alias + "(v)"
		value = alias + ".v"
	} else {
		from = "json_each(CASE WHEN json_type(" + f.SQLExpr + ") = 'array' THEN " + f.SQLExpr + " END) AS " + alias
		// An element that is not an object has no members; json_extract
		// would refuse a scalar's text.
		value = "(CASE WHEN " + alias + ".type = 'object' THEN " + alias + ".value END)"
	}
	var sb strings.Builder
	if node.Op == OpNone {
		sb.WriteString("NOT ")
	}
	sb.WriteString("EXISTS (SELECT 1 FROM " + from)
	if len(node.Children) > 0 {
		nested, err := rebaseElements(node.Children[0], value, map[*FieldSchema]*FieldSchema{})
		if err != nil {
			return "", err
		}
		nb := &SQLBuilder{schema: b.schema, dialect: b.dialect, scope: b.scope, related: b.related, depth: b.depth + 1, visible: b.visible}
		cond, err := nb.buildNode(s, nested)
		if err != nil {
			return "", err
		}
		sb.WriteString(" WHERE " + cond)
	}
	sb.WriteByte(')')
	return sb.String(), nil
}

// rebaseElements is n with its element paths reading value, the element.
func rebaseElements(n *FilterNode, value string, moved map[*FieldSchema]*FieldSchema) (*FilterNode, error) {
	out := *n
	if f := n.Field; f != nil {
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
