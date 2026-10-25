package filter

import "strings"

// rangeBucketProjection generates the CASE expression that maps a column's
// value to a range-bucket key, the placeholder args appended in declaration
// order, and the number of placeholder slots consumed.
//
// Output shape:
//
//	CASE
//	  WHEN <expr> >= $N AND <expr> < $N+1 THEN '0-50'
//	  WHEN <expr> >= $N+2                 THEN '50+'
//	END
//
// Used by BuildFacetQuery's CTE projection.
func rangeBucketProjection(sqlExpr string, ranges []FacetRange, dialect Dialect, paramOffset int) (caseSQL string, args []any, paramsConsumed int) {
	var b strings.Builder
	paramIdx := paramOffset

	b.WriteString("CASE")
	for _, r := range ranges {
		b.WriteString(" WHEN ")
		switch {
		case r.Min != nil && r.Max != nil:
			paramIdx++
			b.WriteString(sqlExpr)
			b.WriteString(" >= ")
			b.WriteString(dialect.Placeholder(paramIdx))
			args = append(args, *r.Min)
			paramIdx++
			b.WriteString(" AND ")
			b.WriteString(sqlExpr)
			b.WriteString(" < ")
			b.WriteString(dialect.Placeholder(paramIdx))
			args = append(args, *r.Max)
		case r.Min != nil:
			paramIdx++
			b.WriteString(sqlExpr)
			b.WriteString(" >= ")
			b.WriteString(dialect.Placeholder(paramIdx))
			args = append(args, *r.Min)
		case r.Max != nil:
			paramIdx++
			b.WriteString(sqlExpr)
			b.WriteString(" < ")
			b.WriteString(dialect.Placeholder(paramIdx))
			args = append(args, *r.Max)
		}
		b.WriteString(" THEN '")
		b.WriteString(escapeSQLString(r.Key))
		b.WriteString("'")
	}
	b.WriteString(" END")
	return b.String(), args, paramIdx - paramOffset
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
