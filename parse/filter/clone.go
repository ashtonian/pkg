package filter

import (
	"maps"
	"slices"
)

// Clone returns a deep copy of the schema: its fields and every schema they
// reach (relation targets, document elements, array elements), with their
// slices, maps and pointers. A change to the copy changes nothing s parses.
// Validate functions are shared. A field held under two names, or a schema
// reached twice (a relation back to its owner), is one value in the copy as
// in s.
func (s *ResourceSchema) Clone() *ResourceSchema {
	return newSchemaCloner().schema(s)
}

// Clone returns a deep copy of the sort schema.
func (s *SortSchema) Clone() *SortSchema {
	if s == nil {
		return nil
	}
	out := *s
	if s.Fields != nil {
		out.Fields = make(map[string]*SortFieldSchema, len(s.Fields))
		for name, f := range s.Fields {
			out.Fields[name] = f.clone()
		}
	}
	return &out
}

func (f *SortFieldSchema) clone() *SortFieldSchema {
	if f == nil {
		return nil
	}
	out := *f
	out.Path = f.Path.clone()
	return &out
}

// Clone returns a copy of the search schema.
func (s *SearchSchema) Clone() *SearchSchema {
	if s == nil {
		return nil
	}
	out := *s
	out.Fields = slices.Clone(s.Fields)
	return &out
}

func (p *JSONPath) clone() *JSONPath {
	if p == nil {
		return nil
	}
	out := *p
	out.Segments = slices.Clone(p.Segments)
	return &out
}

func (s *FacetSchema) clone() *FacetSchema {
	if s == nil {
		return nil
	}
	out := *s
	out.Ranges = slices.Clone(s.Ranges)
	for i, r := range out.Ranges {
		out.Ranges[i].Min, out.Ranges[i].Max = cloneFloat(r.Min), cloneFloat(r.Max)
	}
	return &out
}

func cloneFloat(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// schemaCloner copies a schema graph once per node, so shared and cyclic
// references stay shared and cyclic in the copy.
type schemaCloner struct {
	schemas map[*ResourceSchema]*ResourceSchema
	fields  map[*FieldSchema]*FieldSchema
}

func newSchemaCloner() *schemaCloner {
	return &schemaCloner{schemas: map[*ResourceSchema]*ResourceSchema{}, fields: map[*FieldSchema]*FieldSchema{}}
}

func (c *schemaCloner) schema(s *ResourceSchema) *ResourceSchema {
	if s == nil {
		return nil
	}
	if out, ok := c.schemas[s]; ok {
		return out
	}
	out := new(ResourceSchema)
	*out = *s
	c.schemas[s] = out
	out.Fields = c.fieldMap(s.Fields)
	out.SystemFields = c.fieldMap(s.SystemFields)
	out.PolicyFields = c.fieldMap(s.PolicyFields)
	out.policyRefused = maps.Clone(s.policyRefused)
	return out
}

func (c *schemaCloner) fieldMap(m map[string]*FieldSchema) map[string]*FieldSchema {
	if m == nil {
		return nil
	}
	out := make(map[string]*FieldSchema, len(m))
	for name, f := range m {
		out[name] = c.field(f)
	}
	return out
}

func (c *schemaCloner) field(f *FieldSchema) *FieldSchema {
	if f == nil {
		return nil
	}
	if out, ok := c.fields[f]; ok {
		return out
	}
	out := new(FieldSchema)
	*out = *f
	c.fields[f] = out
	out.AllowedOperators = slices.Clone(f.AllowedOperators)
	out.EnumValues = slices.Clone(f.EnumValues)
	if f.Relation != nil {
		rel := *f.Relation
		out.Relation = &rel
	}
	out.Related = c.schema(f.Related)
	out.RelatedBy = c.field(f.RelatedBy)
	out.Path = f.Path.clone()
	out.Elements = c.schema(f.Elements)
	out.Element = c.field(f.Element)
	out.FacetSchema = f.FacetSchema.clone()
	if f.GeoConfig != nil {
		geo := *f.GeoConfig
		out.GeoConfig = &geo
	}
	return out
}
