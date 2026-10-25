//go:build unit

package patch_test

import (
	"fmt"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// Example is the README Quick Start: a generated schema validates a JSON
// Patch and a merge patch, and each compiles to one SET clause.
func Example() {
	type Device struct {
		Name   string         `json:"name"   db:"name"   parse:"patch"`
		Tags   []string       `json:"tags"   db:"tags"   parse:"patch"`
		Labels map[string]any `json:"labels" db:"labels" parse:"patch"`
	}
	schema, err := patch.GeneratePatchSchema[Device](core.NewSchemaGenerator())
	if err != nil {
		panic(err)
	}

	ops, err := patch.NewParser(schema, nil).Parse([]byte(
		`[{"op":"replace","path":"/name","value":"lobby"},{"op":"array_append","path":"/tags","value":"hvac"}]`))
	if err != nil {
		panic(err)
	}
	set, err := patch.BuildPatchSQL(ops, filter.PostgresDialect{})
	if err != nil {
		panic(err)
	}
	fmt.Println(set.SetClause)

	// Object values merge into the stored document; null deletes a key.
	merge, err := patch.ParseMergePatch([]byte(`{"labels":{"floor":2,"legacy":null}}`), schema, nil)
	if err != nil {
		panic(err)
	}
	set, err = patch.BuildPatchSQL(merge, filter.SQLiteDialect{})
	if err != nil {
		panic(err)
	}
	fmt.Println(set.SetClause, set.Args)
	// Output:
	// name = $1, tags = array_append(COALESCE(tags, '{}'), $2)
	// labels = json_patch(COALESCE(labels, '{}'), ?1) [{"floor":2,"legacy":null}]
}

func ExampleParser_Parse() {
	schema := &patch.PatchSchema{
		Resource: "devices",
		AllowedPaths: map[string]*patch.PathSchema{
			"/name":   {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString, SQLColumn: "name"},
			"/status": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString, SQLColumn: "status"},
		},
		MaxOperations: 50,
	}

	p := patch.NewParser(schema, nil)
	parsed, err := p.Parse([]byte(`[{"op":"replace","path":"/name","value":"updated"}]`))
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("operations:", len(parsed.Operations))
	fmt.Println("affected:", parsed.AffectedPaths)
	// Output:
	// operations: 1
	// affected: [/name]
}

func ExampleBuildPatchSQL() {
	parsed := &patch.ParsedPatch{
		Operations: []*patch.PatchOperation{
			{Op: patch.PatchOpReplace, Path: "/name", Value: "new", Schema: &patch.PathSchema{SQLColumn: "name", DataType: core.FieldTypeString}},
			{Op: patch.PatchOpReplace, Path: "/status", Value: "active", Schema: &patch.PathSchema{SQLColumn: "status", DataType: core.FieldTypeString}},
		},
	}

	result, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("SET:", result.SetClause)
	fmt.Println("args:", result.Args)
	// Output:
	// SET: name = $1, status = $2
	// args: [new active]
}

func ExampleGeneratePatchSchema() {
	type Device struct {
		ID     string `json:"id"     parse:"filter"`
		Name   string `json:"name"   parse:"patch" patch_ops:"replace" db:"device_name"`
		Status string `json:"status" parse:"patch" patch_ops:"replace" enum:"active,offline"`
	}

	gen := core.NewSchemaGenerator()
	schema, err := patch.GeneratePatchSchema[Device](gen)
	if err != nil {
		fmt.Println("error:", err)
		return
	}

	fmt.Println("has /name:", schema.AllowedPaths["/name"] != nil)
	fmt.Println("name column:", schema.AllowedPaths["/name"].SQLColumn)
	fmt.Println("has /status:", schema.AllowedPaths["/status"] != nil)
	fmt.Println("has /id:", schema.AllowedPaths["/id"] != nil)
	// Output:
	// has /name: true
	// name column: device_name
	// has /status: true
	// has /id: false
}

func ExampleApplyPatchPolicyConstraints() {
	parsed := &patch.ParsedPatch{
		Operations: []*patch.PatchOperation{
			{Op: patch.PatchOpReplace, Path: "/status", Value: "active"},
		},
	}

	pc := &patch.PatchPolicyConstraints{
		ForbiddenPaths: []string{"/tenant_id"},
		ValueConstraints: map[string]*patch.ValueConstraint{
			"/status": {AllowedValues: []any{"active", "inactive"}},
		},
	}

	err := patch.ApplyPatchPolicyConstraints(parsed, pc, nil)
	fmt.Println("allowed:", err == nil)

	forbidden := &patch.ParsedPatch{
		Operations: []*patch.PatchOperation{
			{Op: patch.PatchOpReplace, Path: "/tenant_id", Value: "hack"},
		},
	}
	err = patch.ApplyPatchPolicyConstraints(forbidden, pc, nil)
	fmt.Println("forbidden:", strings.Contains(err.Error(), "forbidden"))
	// Output:
	// allowed: true
	// forbidden: true
}
