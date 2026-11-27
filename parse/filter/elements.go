package filter

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// elementColumn is the SQL an unbound element field or element path reads
// until a predicate on the elements binds it to its own element alias. It
// only labels the placeholder: FieldSchema.role, not this text, marks one.
const elementColumn = "elem__"

// documentElements derives the element schema of a JSON array document
// the members of a slice-of-struct's elements tagged
// `parse:"filter"`, or the `elements` tag's paths (grammar of `paths`) for
// any JSON array. A document with element paths takes `$some` / `$none`,
// whose filters one element must satisfy together. Nil when it has none.
func documentElements(g *core.SchemaGenerator, field *core.Field, doc *FieldSchema) (*ResourceSchema, error) {
	var decls []PathDecl
	var kinds []reflect.Kind
	t := field.Type()
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var elem reflect.Type
	if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
		elem = t.Elem()
		for elem.Kind() == reflect.Pointer {
			elem = elem.Elem()
		}
		if core.MemberTypeOf(elem).Type == core.FieldTypeObject {
			d, k, err := structPaths(g, elem, nil, core.CapabilityFilter)
			if err != nil {
				return nil, fmt.Errorf("elements: %w", err)
			}
			decls, kinds = d, k
		}
	}
	elements := field.Tags().Elements
	if elements != "" {
		d, err := parsePathsTag(elements)
		if err != nil {
			return nil, fmt.Errorf("elements: %w", err)
		}
		for _, decl := range d {
			if elem != nil {
				if err := checkDeclaredMember(g, elem, decl.Path); err != nil {
					return nil, fmt.Errorf("elements: %w", err)
				}
			}
			decls = append(decls, decl)
			kinds = append(kinds, kindOf(decl.elemOrType()))
		}
	}
	if len(decls) == 0 {
		return nil, nil
	}
	placeholder := &FieldSchema{Expr: elementColumn, Storage: StorageJSONB}
	fields := make(map[string]*FieldSchema, len(decls))
	for i, d := range decls {
		segs := strings.Split(d.Path, ".")
		d.Facet = nil // elements have no facets: a facet counts rows
		f, err := newPathField(placeholder, segs, d, kinds[i])
		if err != nil {
			return nil, fmt.Errorf("element path %s: %w", d.Path, err)
		}
		f.role = roleElementPath
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

// DocumentElements is a JSON array document's elements, decoded as
// PathValue decodes documents; nil for a document that is not an array. A
// backend with its own evaluator of `$some` / `$none` tests the node's
// condition, n.Children[0], on each element e as Evaluate does:
// Evaluate(n.Children[0], ElementRow{Element: e}), or its own evaluator
// reading each field of the condition through that row. A node with no
// condition (`{}`) has no children and holds for any element. An element
// that is not an object, a string included, has no members.
func DocumentElements(doc any) ([]any, error) {
	root, err := decodeDocument(doc)
	if err != nil {
		return nil, err
	}
	elems, _ := root.([]any)
	return elems, nil
}
