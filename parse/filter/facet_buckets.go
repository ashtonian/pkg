package filter

import (
	"fmt"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// rangeBucketProjection generates the CASE expression that maps field's
// value to a range-bucket key, the placeholder args appended in declaration
// order, and the number of placeholder slots consumed. A decimal field
// compares with its bounds exactly, through Dialect.DecimalOrder.
//
// Output shape:
//
//	CASE
//	  WHEN <expr> >= $N AND <expr> < $N+1 THEN '0-50'
//	  WHEN <expr> >= $N+2                 THEN '50+'
//	END
//
// Used by BuildFacetQuery's CTE projection.
func rangeBucketProjection(field *FieldSchema, ranges []FacetRange, dialect Dialect, paramOffset int) (caseSQL string, args []any, paramsConsumed int, err error) {
	expr := FieldExpr(dialect, field)
	param := dialect.Placeholder
	arg := func(bound float64) (any, error) { return bound, nil }
	if field.DataType == core.FieldTypeDecimal {
		expr = dialect.DecimalOrder(expr)
		param = func(n int) string { return dialect.DecimalOrder(dialect.DecimalParam(dialect.Placeholder(n))) }
		arg = func(bound float64) (any, error) { return core.DecimalText(bound) }
	}
	var b strings.Builder
	paramIdx := paramOffset
	compare := func(op string, bound float64) error {
		v, err := arg(bound)
		if err != nil {
			return fmt.Errorf("facet %s: range bound: %w", field.ExternalName, err)
		}
		paramIdx++
		args = append(args, v)
		b.WriteString(expr + " " + op + " " + param(paramIdx))
		return nil
	}
	b.WriteString("CASE")
	for _, r := range ranges {
		b.WriteString(" WHEN ")
		if r.Min != nil {
			if err := compare(">=", *r.Min); err != nil {
				return "", nil, 0, err
			}
		}
		if r.Min != nil && r.Max != nil {
			b.WriteString(" AND ")
		}
		if r.Max != nil {
			if err := compare("<", *r.Max); err != nil {
				return "", nil, 0, err
			}
		}
		b.WriteString(" THEN '")
		b.WriteString(escapeSQLString(r.Key))
		b.WriteString("'")
	}
	b.WriteString(" END")
	return b.String(), args, paramIdx - paramOffset, nil
}

// escapeSQLString escapes single quotes for safe inclusion in a literal.
// Range bucket keys are author-defined (not user input), but we still
// escape defensively so a malformed key doesn't break the query.
func escapeSQLString(s string) string {
	if !strings.ContainsRune(s, '\'') {
		return s
	}
	return strings.ReplaceAll(s, "'", "''")
}
