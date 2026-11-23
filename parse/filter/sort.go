package filter

import (
	"fmt"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// --- Sort Parser ---

// ParseSort parses a sort parameter string.
func ParseSort(schema *SortSchema, sortParam string) (*SortResult, error) {
	if schema == nil {
		return nil, newSortError(ECodeSortFieldNotAllowed, "no sort schema provided")
	}

	maxFields := schema.MaxFields
	if maxFields == 0 {
		maxFields = 3
	}

	if sortParam == "" {
		if schema.DefaultField == "" {
			return &SortResult{}, nil
		}
		fieldSchema, ok := schema.Fields[schema.DefaultField]
		if !ok {
			return &SortResult{}, nil
		}
		dir := schema.DefaultDir
		if dir == "" {
			dir = SortAsc
		}
		return &SortResult{
			Fields: []SortField{{Name: schema.DefaultField, Schema: fieldSchema, Direction: dir}},
		}, nil
	}

	parts := strings.Split(sortParam, ",")
	if len(parts) > maxFields {
		return nil, &core.ParseError{
			Kind:    core.ParseErrorKindSort,
			Code:    ECodeSortFieldLimit.String(),
			Message: fmt.Sprintf("sort field count %d exceeds maximum %d", len(parts), maxFields),
		}
	}

	fields := make([]SortField, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		dir := SortAsc
		name := part
		if strings.HasPrefix(part, "-") {
			dir = SortDesc
			name = part[1:]
		} else if strings.HasPrefix(part, "+") {
			name = part[1:]
		}
		fieldSchema, ok := schema.Fields[name]
		if !ok {
			return nil, &core.ParseError{
				Kind:    core.ParseErrorKindSort,
				Code:    ECodeSortFieldNotAllowed.String(),
				Message: fmt.Sprintf("sort field '%s' is not allowed", name),
				Details: map[string]any{core.DetailField: name},
			}
		}
		fields = append(fields, SortField{Name: name, Schema: fieldSchema, Direction: dir})
	}

	return &SortResult{Fields: fields}, nil
}

// ToSQL renders the sort's ORDER BY in dialect d, as a keyset orders it:
// code-point text, NULL greatest on nullable keys, paths through
// Dialect.PathExpr. A list query appends its key columns itself.
func (s *SortResult) ToSQL(d Dialect) string {
	if s == nil || len(s.Fields) == 0 {
		return ""
	}
	return OrderByKeys(d, Keyset(s), false)
}
