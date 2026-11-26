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
// A field promoted through an unexported embedded pointer is never patched:
// reflection can neither allocate that pointer nor point it at a copy, so
// generation refuses parse:"patch" on it, and [Applier.CheckSchema] a path
// added by hand that reaches it.
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
//	// result.Args = ["new-name", "active", core.FormatTimestamp(now)]
//
// [WithChangePredicate] also renders [PatchSQLResult.ChangePredicate], true
// for a row the patch's columns change; ANDed into the UPDATE's WHERE, it
// writes, stamps and counts only those rows.
//
// patch names no database: SQL whose form differs between databases is a
// method of the dialect it compiles for, [SQLDialect] for a SET clause and
// its preconditions and [RelationDialect] for membership statements, so a
// SQL provider is one type implementing them. The set is sealed: both embed
// the module's internal dialect kind, so the parse/filter dialects implement
// them, and a new provider is a type there.
//
// # Policy Enforcement
//
// [ApplyPatchPolicyConstraints] validates parsed patches against policy rules:
// forbidden paths, allowed/denied values, regex patterns, and custom
// validators. [NewPatchPolicyConstraints] checks every pattern when the policy
// is built: a token is a literal, * (one token) or ** (zero or more), so
// "/internal/**" forbids /internal and everything below it.
//
//	pc, err := patch.NewPatchPolicyConstraints(
//	    patch.ForbidPaths("/tenant_id", "/internal/**"),
//	    patch.ConstrainValues("/status", patch.ValueConstraint{AllowedValues: []any{"active", "inactive"}}),
//	)
//	err = patch.ApplyPatchPolicyConstraints(parsed, pc, subject)
package patch
