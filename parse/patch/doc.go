// Package patch provides JSON Patch (RFC 6902) and Merge Patch (RFC 7396)
// parsing with struct-tag-driven schema validation, SQL SET clause generation,
// and policy enforcement.
//
// # Struct Tags
//
// Patch schema generation is driven by struct tags. The following tags are
// recognized by [GeneratePatchSchema]:
//
//	parse:"patch"           — include in patch schema
//	patch_ops:"replace,add" — allowed operations (add, remove, replace, move, copy, test, array_append)
//	enum:"active,inactive"  — restrict values to this set
//	db:"column_name"        — SQL column name for SET clause (default: snake_case of field name)
//
// The tag key for field names (default "json") and column names (default "db")
// can be changed via [core.WithFieldNameTag] and [core.WithColumnTag].
//
// # Parsing
//
// [Parser] validates JSON Patch documents against a [PatchSchema]:
//
//	p := patch.NewParser(schema, nil)
//	parsed, err := p.Parse(body) // validates operations, pointers, nullability, values, enums
//
// [ParseMergePatch] handles RFC 7396 Merge Patch (plain JSON with null = remove):
//
//	parsed, err := patch.ParseMergePatch(body, schema, nil)
//
// # SQL Generation
//
// [BuildPatchSQL] converts a [ParsedPatch] into a parameterized SQL SET clause,
// one expression per column; operations SQL cannot express (move, copy,
// test, nested paths) are rejected, and merge-patch objects merge into the
// stored document:
//
//	result, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{},
//	    patch.WithAutoTimestamp("updated_at"),
//	)
//	// result.SetClause = "name = $1, status = $2, updated_at = $3"
//	// result.Args = ["new-name", "active", <time.Time>]
//
// # Policy Enforcement
//
// [ApplyPatchPolicyConstraints] validates parsed patches against policy rules:
// forbidden paths, allowed/denied values, regex patterns, and custom validators.
//
//	pc := &patch.PatchPolicyConstraints{
//	    ForbiddenPaths: []string{"/tenant_id", "/internal_*"},
//	    ValueConstraints: map[string]*patch.ValueConstraint{
//	        "/status": {AllowedValues: []any{"active", "inactive"}},
//	    },
//	}
//	err := patch.ApplyPatchPolicyConstraints(parsed, pc, subject)
package patch
