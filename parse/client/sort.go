package client

import "strings"

// Order is one sort key and its direction, from a SortKey's Asc or Desc.
type Order struct {
	field string
	desc  bool
}

// String is the order as the sort parameter spells it: field, or -field.
func (o Order) String() string {
	if o.desc {
		return "-" + o.field
	}
	return o.field
}

// Sort is a list's sort order, most significant key first.
type Sort []Order

// String is the sort parameter: the orders joined by commas.
func (s Sort) String() string {
	parts := make([]string, len(s))
	for i, o := range s {
		parts[i] = o.String()
	}
	return strings.Join(parts, ",")
}
