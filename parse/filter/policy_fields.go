package filter

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// ErrAmbiguousPolicyField is a server policy constraint whose Field names
// more than one field: one field's name and another's column, a column two
// fields hold, or a Fields entry and a different PolicyFields entry. The
// constraint is refused rather than resolved to either.
var ErrAmbiguousPolicyField = errors.New("filter: a policy constraint's field names more than one field")

// PolicyField resolves name as a server policy constraint's Field: a system
// field first, then a request field (Fields) or a PolicyFields entry. ok is
// false for a name no field has. err is set for a name a policy may not use:
// ErrAmbiguousPolicyField, or the error of a field whose type a filter field
// could not take.
func (s *ResourceSchema) PolicyField(name string) (field *FieldSchema, ok bool, err error) {
	if f, ok := s.SystemFields[name]; ok {
		return f, true, nil
	}
	if err, ok := s.policyRefused[name]; ok {
		return nil, true, err
	}
	request, inFields := s.Fields[name]
	policy, inPolicy := s.PolicyFields[name]
	switch {
	case inFields && inPolicy && request != policy:
		return nil, true, fmt.Errorf("policy field %q is a request field and a different PolicyFields entry: %w", name, ErrAmbiguousPolicyField)
	case inFields:
		return request, true, nil
	case inPolicy:
		return policy, true, nil
	}
	return nil, false, nil
}

// policyCandidate is a field of the walk as a policy may name it: by its
// name, or by its column.
type policyCandidate struct {
	goName string
	// name is a request field's name or a visible field's external name;
	// empty for a hidden field.
	name string
	// column is empty for a relation, a path, or a column the generator
	// cannot name.
	column  string
	request bool
	// field is nil when err holds why a filter field could not type it.
	field *FieldSchema
	err   error
}

func requestCandidate(field *core.Field, fs *FieldSchema) policyCandidate {
	return policyCandidate{goName: field.GoName, name: fs.ExternalName, column: fs.Column, request: true, field: fs}
}

// policyColumn is the candidate of a column a request cannot filter by. ok
// is false for a field that is not a column. A field whose type a filter
// field could not take keeps the error for a policy that names it rather
// than failing the schema: a field no request filters by must not stop the
// resource from building.
func policyColumn(g *core.SchemaGenerator, table string, field *core.Field) (policyCandidate, bool) {
	if field.Tags.Relation != "" {
		return policyCandidate{}, false
	}
	column, ok, err := g.FieldColumn(field)
	if err == nil && !ok {
		return policyCandidate{}, false
	}
	c := policyCandidate{goName: field.GoName, name: field.External, column: column}
	if err == nil {
		c.field, err = policyFieldSchema(g, table, field, column)
	}
	if err != nil {
		c.field, c.err = nil, fmt.Errorf("field %s: %w", field.GoName, err)
	}
	return c, true
}

// policyFieldSchema types and stores a column as a filter field over it
// would be. Its operators are every one its type supports: a policy is not
// held to request operators.
func policyFieldSchema(g *core.SchemaGenerator, table string, field *core.Field, column string) (*FieldSchema, error) {
	info, err := g.FieldType(field)
	if err != nil {
		return nil, err
	}
	fs := &FieldSchema{
		ExternalName: cmp.Or(field.External, column),
		Column:       column,
		DataType:     info.Type,
		NumberKind:   info.NumberKind,
		Elem:         info.Elem,
		EnumValues:   slices.Clone(field.Tags.Enum),
	}
	if field.Tags.Geo != "" {
		fs.DataType = core.FieldTypeGeo
		if fs.GeoConfig, err = parseGeoTag(&field.Tags); err != nil {
			return nil, err
		}
	}
	// FieldColumn's base, so ColumnExpr names the same column: the external
	// name, or a hidden field's Go name.
	if fs.SQLExpr, err = g.ColumnExpr(table, cmp.Or(field.External, field.GoName), fs.DataType, field.Tags.Column); err != nil {
		return nil, err
	}
	if err := applyStorage(g, fs, field); err != nil {
		return nil, err
	}
	fs.AllowedOperators = OperatorsFor(fs.DataType, false)
	if isScalarArray(fs) {
		fs.Element = newElement(fs)
	}
	return fs, nil
}

// indexPolicyNames resolves the names policies use besides Fields: the
// external name of each field without the filter capability, and every
// field's column. A name is refused when it designates more than one field
// (ambiguous) or names a field a filter field could not type. The result
// depends only on the candidates, not on the order they come in.
func indexPolicyNames(candidates []policyCandidate) (map[string]*FieldSchema, map[string]error) {
	byName := make(map[string][]*policyCandidate)
	byColumn := make(map[string][]*policyCandidate)
	var names []string
	for i := range candidates {
		c := &candidates[i]
		if c.name != "" {
			byName[c.name] = append(byName[c.name], c)
			if !c.request {
				names = append(names, c.name)
			}
		}
		if c.column != "" {
			byColumn[c.column] = append(byColumn[c.column], c)
			names = append(names, c.column)
		}
	}

	fields := make(map[string]*FieldSchema)
	refused := make(map[string]error)
	seen := make(map[string]bool)
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		named, columned := byName[name], byColumn[name]
		if ambiguous(name, named, columned) {
			refused[name] = ambiguityError(name, named, columned)
			continue
		}
		c := slices.Concat(named, columned)[0]
		switch {
		case c.err != nil:
			refused[name] = fmt.Errorf("policy field %q: %w", name, c.err)
		case !c.request || c.name != name: // a request field by its name is in Fields
			fields[name] = c.field
		}
	}
	return fields, refused
}

// ambiguous reports a name that designates more than one field: the names
// of two, one's name and another's column, or a column two hold that is
// neither's name. Fields sharing the name's column are one target.
func ambiguous(name string, named, columned []*policyCandidate) bool {
	switch len(named) {
	case 0:
		return len(columned) > 1
	case 1:
		return len(columned) > 0 && named[0].column != name
	}
	return true
}

func ambiguityError(name string, named, columned []*policyCandidate) error {
	var claims []string
	for _, c := range named {
		claims = append(claims, "the name of "+c.describe())
	}
	for _, c := range columned {
		if !slices.Contains(named, c) {
			claims = append(claims, "the column of "+c.describe())
		}
	}
	return fmt.Errorf("policy field %q is %s: %w", name, strings.Join(claims, " and "), ErrAmbiguousPolicyField)
}

func (c *policyCandidate) describe() string {
	switch {
	case c.field != nil && c.field.Relation != nil:
		return "relation field " + c.goName
	case c.field != nil && c.field.Path != nil:
		return fmt.Sprintf("path %s of field %s", c.name, c.goName)
	case c.column != "":
		return fmt.Sprintf("field %s (column %s)", c.goName, c.column)
	}
	return "field " + c.goName
}
