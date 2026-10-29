package filter

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// MaxPathDepth bounds how many members deep a declared path may reach.
const MaxPathDepth = 8

// PathDecl declares a typed member of a document field, for paths a struct
// tag cannot carry — such as a tenant's custom fields, declared per request.
type PathDecl struct {
	// Path is the document field's name, then its members: "labels.region".
	Path string
	Type core.FieldType
	// Ops are the operators allowed on the path; nil means the type's
	// defaults with the NULL tests (a path can always be absent).
	Ops     []FilterOperator
	Indexed bool
	// Sort also makes the path a sort key (SortSchema.WithPaths).
	Sort bool
	// Facet makes the path facetable with this configuration (terms or
	// range), as a column's `facet` tag does.
	Facet *FacetSchema
	Label string
}

// WithPaths returns a copy of s with decls added as fields; s is unchanged,
// so a request can declare paths without touching the shared schema. A
// path's document field must be a filterable json or object field of s.
func (s *ResourceSchema) WithPaths(decls ...PathDecl) (*ResourceSchema, error) {
	out := *s
	out.Fields = maps.Clone(s.Fields)
	for _, d := range decls {
		name, members, _ := strings.Cut(d.Path, ".")
		doc, ok := s.Fields[name]
		if !ok || !core.IsDocument(doc.DataType) || doc.Path != nil {
			return nil, fmt.Errorf("path %q: %q is not a document field of %s", d.Path, name, s.Name)
		}
		if members == "" {
			return nil, fmt.Errorf("path %q names no member", d.Path)
		}
		f, err := newPathField(doc, strings.Split(members, "."), d.Type, kindOf(d.Type), d.Ops)
		if err != nil {
			return nil, fmt.Errorf("path %q: %w", d.Path, err)
		}
		f.Indexed, f.Label = d.Indexed, d.Label
		setPathFacet(f, d.Facet)
		if _, dup := out.Fields[f.ExternalName]; dup {
			return nil, fmt.Errorf("path %q is already a field of %s", d.Path, s.Name)
		}
		out.Fields[f.ExternalName] = f
	}
	return &out, nil
}

func kindOf(t core.FieldType) reflect.Kind {
	switch t {
	case core.FieldTypeInt:
		return reflect.Int64
	case core.FieldTypeFloat:
		return reflect.Float64
	}
	return reflect.Invalid
}

// storageOf reads a document field's storage tag; scalar reports a
// "scalar:<type>" declaration, a struct type the database holds as a scalar.
func storageOf(tag string) (storage JSONStorage, scalar core.FieldType, err error) {
	switch {
	case tag == "":
		return StorageJSONB, "", nil
	case strings.HasPrefix(tag, "scalar:"):
		t := core.FieldType(strings.TrimPrefix(tag, "scalar:"))
		if !pathTypes[t] {
			return "", "", fmt.Errorf("storage %q: a scalar is %s", tag, pathTypeNames)
		}
		return "", t, nil
	}
	s := JSONStorage(tag)
	switch s {
	case StorageJSONB, StorageJSON, StorageText:
		return s, "", nil
	}
	return "", "", fmt.Errorf("unknown storage %q (jsonb, json, text or scalar:<type>)", tag)
}

// newPathField builds a document member's field schema; ops nil takes the
// type's defaults with the NULL tests.
func newPathField(doc *FieldSchema, segs []string, t core.FieldType, kind reflect.Kind, ops []FilterOperator) (*FieldSchema, error) {
	storage := StorageJSONB
	if doc.Storage != "" {
		storage = doc.Storage
	}
	p := &JSONPath{Column: doc.SQLExpr, Segments: segs, Storage: storage}
	if err := p.validate(t, MaxPathDepth); err != nil {
		return nil, err
	}
	if ops == nil {
		ops = slices.Clone(defaultFilterOps[t])
		for _, op := range []FilterOperator{OpNull, OpNotNull, OpExists} {
			if !slices.Contains(ops, op) {
				ops = append(ops, op)
			}
		}
	}
	f := &FieldSchema{
		ExternalName:     doc.ExternalName + "." + strings.Join(segs, "."),
		DataType:         t,
		NumberKind:       kind,
		AllowedOperators: ops,
		Path:             p,
	}
	if err := checkOperators(f); err != nil {
		return nil, err
	}
	return f, nil
}

// parsePathsTag reads a `paths` tag: entries separated by ";", each
// "member.path:type[:caps]" with caps joined by "+" from filter, sort,
// facet (a terms facet) and indexed (filter is implied).
func parsePathsTag(tag string) ([]PathDecl, error) {
	var out []PathDecl
	for entry := range strings.SplitSeq(tag, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return nil, fmt.Errorf("paths entry %q: want member:type[:caps]", entry)
		}
		d := PathDecl{Path: strings.TrimSpace(parts[0]), Type: core.FieldType(strings.TrimSpace(parts[1]))}
		if len(parts) == 3 {
			for c := range strings.SplitSeq(parts[2], "+") {
				switch strings.TrimSpace(c) {
				case "filter":
				case "sort":
					d.Sort = true
				case "facet":
					d.Facet = &FacetSchema{Type: FacetTerms}
				case "indexed":
					d.Indexed = true
				default:
					return nil, fmt.Errorf("paths entry %q: unknown capability %q", entry, c)
				}
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// structPaths derives the paths of a struct-typed document: every member
// tagged parse:"<want>" (filter or sort), nested structs recursed when
// tagged too, named by their field-name tag.
func structPaths(g *core.SchemaGenerator, t reflect.Type, prefix []string, want string) ([]PathDecl, []reflect.Kind, error) {
	walk, err := g.StructFields(t)
	if err != nil {
		return nil, nil, err
	}
	var decls []PathDecl
	var kinds []reflect.Kind
	for _, m := range walk.Fields {
		caps := m.Tags.Parse
		if want == "filter" && !caps.Filter || want == "sort" && !caps.Sort {
			continue
		}
		name := m.External
		if name == "" {
			continue
		}
		segs := append(slices.Clone(prefix), name)
		if len(segs) > MaxPathDepth {
			return nil, nil, fmt.Errorf("member %s is more than %d members deep", strings.Join(segs, "."), MaxPathDepth)
		}
		info := core.TypeOf(m.Type)
		switch {
		case pathTypes[info.Type]:
			d := PathDecl{Path: strings.Join(segs, "."), Type: info.Type, Indexed: m.Tags.Indexed, Label: m.Tags.Label, Sort: want == "sort"}
			if want == "filter" && caps.Facet {
				fs, err := ParseFacetTag(m.Tags.Facet)
				if err != nil {
					return nil, nil, fmt.Errorf("member %s: %w", d.Path, err)
				}
				if fs == nil {
					fs = &FacetSchema{Type: FacetTerms}
				}
				d.Facet = fs
			}
			if opsTag := m.Tags.FilterOps; opsTag != "" {
				ops, err := parseOpsTag(opsTag)
				if err != nil {
					return nil, nil, fmt.Errorf("member %s: %w", d.Path, err)
				}
				d.Ops = ops
			}
			decls = append(decls, d)
			kinds = append(kinds, info.NumberKind)
		case info.Type == core.FieldTypeObject:
			nested, nestedKinds, err := structPaths(g, m.Type, segs, want)
			if err != nil {
				return nil, nil, err
			}
			decls = append(decls, nested...)
			kinds = append(kinds, nestedKinds...)
		default:
			return nil, nil, fmt.Errorf("member %s has type %s; paths hold scalars and nested structs", strings.Join(segs, "."), info.Type)
		}
	}
	return decls, kinds, nil
}

func parseOpsTag(tag string) ([]FilterOperator, error) {
	var ops []FilterOperator
	for p := range strings.SplitSeq(tag, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		op := FilterOperator(p)
		if !ValidFilterOps[op] {
			return nil, fmt.Errorf("unknown filter operator %q", p)
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// documentPathFields builds the path fields a document field declares.
func documentPathFields(g *core.SchemaGenerator, field *core.Field, doc *FieldSchema) ([]*FieldSchema, error) {
	var decls []PathDecl
	var kinds []reflect.Kind
	if doc.DataType == core.FieldTypeObject {
		d, k, err := structPaths(g, field.Type, nil, "filter")
		if err != nil {
			return nil, err
		}
		decls, kinds = d, k
	}
	if tag := field.Tags.Paths; tag != "" {
		d, err := parsePathsTag(tag)
		if err != nil {
			return nil, err
		}
		for _, decl := range d {
			decls = append(decls, decl)
			kinds = append(kinds, kindOf(decl.Type))
		}
	}
	out := make([]*FieldSchema, 0, len(decls))
	for i, d := range decls {
		f, err := newPathField(doc, strings.Split(d.Path, "."), d.Type, kinds[i], d.Ops)
		if err != nil {
			return nil, fmt.Errorf("path %s: %w", d.Path, err)
		}
		f.Indexed, f.Label = d.Indexed, d.Label
		setPathFacet(f, d.Facet)
		out = append(out, f)
	}
	return out, nil
}

func fieldStorage(g *core.SchemaGenerator, field *core.Field) (string, error) {
	info, err := g.FieldType(field)
	if err != nil {
		return "", err
	}
	tag := field.Tags.Storage
	if info.SQLType.IsJSON() {
		storage := string(info.SQLType)
		if tag != "" && tag != storage {
			return "", fmt.Errorf("storage %q conflicts with declared column type %q", tag, storage)
		}
		return storage, nil
	}
	return tag, nil
}

// scalarStorage is the scalar type a document field's storage tag declares
// (`storage:"scalar:<type>"`), or "" when the field is stored as a document.
func scalarStorage(g *core.SchemaGenerator, field *core.Field) (core.FieldType, error) {
	tag, err := fieldStorage(g, field)
	if err != nil || tag == "" {
		return "", err
	}
	_, scalar, err := storageOf(tag)
	return scalar, err
}

// applyStorage applies a document field's storage declaration: the JSON storage of
// its paths, or a scalar type for a struct the database holds as a scalar
// (which then has scalar operators and no paths).
func applyStorage(g *core.SchemaGenerator, fs *FieldSchema, field *core.Field) error {
	tag, err := fieldStorage(g, field)
	if err != nil {
		return err
	}
	if tag == "" {
		return nil
	}
	if !core.IsDocument(fs.DataType) {
		return fmt.Errorf("storage applies to json and object fields, not %s", fs.DataType)
	}
	storage, scalar, err := storageOf(tag)
	if err != nil {
		return err
	}
	if scalar == "" {
		fs.Storage = storage
		return nil
	}
	if field.Tags.Paths != "" {
		return fmt.Errorf("a scalar (%s) has no paths", tag)
	}
	fs.DataType, fs.NumberKind = scalar, kindOf(scalar)
	if field.Tags.FilterOps == "" {
		fs.AllowedOperators = slices.Clone(defaultFilterOps[scalar])
	}
	return nil
}

// docSortPaths builds the path sort keys a document field declares: struct
// members tagged parse:"sort" and `paths` entries with the sort capability.
func docSortPaths(g *core.SchemaGenerator, field *core.Field, docType core.FieldType, sqlExpr, column string) (map[string]*SortFieldSchema, error) {
	tag, err := fieldStorage(g, field)
	if err != nil {
		return nil, err
	}
	var decls []PathDecl
	var kinds []reflect.Kind
	if docType == core.FieldTypeObject {
		d, k, err := structPaths(g, field.Type, nil, "sort")
		if err != nil {
			return nil, err
		}
		decls, kinds = d, k
	}
	if tag := field.Tags.Paths; tag != "" {
		d, err := parsePathsTag(tag)
		if err != nil {
			return nil, err
		}
		for _, decl := range d {
			if decl.Sort {
				decls = append(decls, decl)
				kinds = append(kinds, kindOf(decl.Type))
			}
		}
	}
	if len(decls) == 0 {
		return nil, nil
	}
	storage, scalar, err := storageOf(tag)
	if err != nil {
		return nil, err
	}
	if scalar != "" {
		return nil, fmt.Errorf("a scalar (%s) has no paths", tag)
	}
	name := field.External
	out := make(map[string]*SortFieldSchema, len(decls))
	for i, d := range decls {
		key, err := newPathSortField(sqlExpr, column, storage, strings.Split(d.Path, "."), d.Type, kinds[i], d.Indexed)
		if err != nil {
			return nil, fmt.Errorf("path %s: %w", d.Path, err)
		}
		out[name+"."+d.Path] = key
	}
	return out, nil
}

func newPathSortField(sqlExpr, column string, storage JSONStorage, segs []string, t core.FieldType, kind reflect.Kind, indexed bool) (*SortFieldSchema, error) {
	if storage == "" {
		storage = StorageJSONB
	}
	p := &JSONPath{Column: sqlExpr, Segments: segs, Storage: storage}
	if err := p.validate(t, MaxPathDepth); err != nil {
		return nil, err
	}
	if column == "" {
		return nil, fmt.Errorf("document %s has no row column to resume a cursor from", sqlExpr)
	}
	return &SortFieldSchema{Column: column, DataType: t, NumberKind: kind, Nullable: true, Indexed: indexed, Path: p}, nil
}

// WithPaths returns a copy of s with the decls marked Sort added as sort
// keys, their documents taken from fields (the resource's filter schema); s
// is unchanged.
func (s *SortSchema) WithPaths(fields *ResourceSchema, decls ...PathDecl) (*SortSchema, error) {
	out := *s
	out.Fields = maps.Clone(s.Fields)
	for _, d := range decls {
		if !d.Sort {
			continue
		}
		name, members, _ := strings.Cut(d.Path, ".")
		doc, ok := fields.Fields[name]
		if !ok || !core.IsDocument(doc.DataType) || doc.Path != nil {
			return nil, fmt.Errorf("sort path %q: %q is not a document field", d.Path, name)
		}
		if members == "" {
			return nil, fmt.Errorf("sort path %q names no member", d.Path)
		}
		if _, dup := out.Fields[d.Path]; dup {
			return nil, fmt.Errorf("sort path %q is already a sort key", d.Path)
		}
		key, err := newPathSortField(doc.SQLExpr, (&SortFieldSchema{SQLExpr: doc.SQLExpr}).RowColumn(), doc.Storage, strings.Split(members, "."), d.Type, kindOf(d.Type), d.Indexed)
		if err != nil {
			return nil, fmt.Errorf("sort path %q: %w", d.Path, err)
		}
		out.Fields[d.Path] = key
	}
	return &out, nil
}

// setPathFacet makes a path facetable with facet's configuration; its SQL
// is the path's value expression, rendered per dialect.
func setPathFacet(f *FieldSchema, facet *FacetSchema) {
	if facet == nil {
		return
	}
	cfg := *facet
	cfg.Field, cfg.SQLExpr = f.ExternalName, ""
	f.Facetable, f.FacetSchema = true, &cfg
}
