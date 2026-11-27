//go:build unit || integration

package patchtest

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

// schema builds a test schema, which must be valid, holding at most
// maxOperations operations (0: any number).
func schema(resource string, maxOperations int, paths map[string]*patch.PathSchema) *patch.PatchSchema {
	s, err := patch.NewPatchSchema(resource, paths)
	if err != nil {
		panic(err)
	}
	s.MaxOperations = maxOperations
	return s
}

// ChangeSchema models a row of every kind of column a change predicate
// compares. name and email are case-insensitive columns, mood an enum and
// span an interval on PostgreSQL, raw a declared json (not jsonb) column
// there; price pads to a scale, amount does not.
func ChangeSchema() *patch.PatchSchema {
	ops := []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpAdd, patch.PatchOpRemove}
	path := func(t core.FieldType, column string) *patch.PathSchema {
		ps := &patch.PathSchema{AllowedOps: ops, DataType: t, SQLColumn: column}
		return ps
	}
	tags := path(core.FieldTypeArray, "tags")
	tags.AllowedOps = append(tags.AllowedOps, patch.PatchOpArrayAppend)
	tags.GoType = reflect.TypeFor[[]string]()
	raw := path(core.FieldTypeJSON, "raw")
	raw.SQLType = core.SQLJSON
	return schema("things", 50, map[string]*patch.PathSchema{
		"/name":       path(core.FieldTypeString, "name"),
		"/email":      path(core.FieldTypeString, "email"),
		"/mood":       path(core.FieldTypeString, "mood"),
		"/rank":       path(core.FieldTypeInt, "rank"),
		"/price":      path(core.FieldTypeDecimal, "price"),
		"/amount":     path(core.FieldTypeDecimal, "amount"),
		"/ratio":      path(core.FieldTypeFloat, "ratio"),
		"/span":       path(core.FieldTypeString, "span"),
		"/at":         path(core.FieldTypeTimestamp, "at"),
		"/doc":        path(core.FieldTypeJSON, "doc"),
		"/raw":        raw,
		"/tags":       tags,
		"/version":    path(core.FieldTypeInt, "version"),
		"/updated_at": path(core.FieldTypeTimestamp, "updated_at"),
	})
}

// ChangePatch parses body against ChangeSchema: a JSON Patch when it is an
// array, a merge patch otherwise.
func ChangePatch(t testing.TB, body string) *patch.ParsedPatch {
	t.Helper()
	if strings.HasPrefix(body, "[") {
		parsed, err := patch.NewParser(ChangeSchema(), nil).Parse([]byte(body))
		require.NoError(t, err, body)
		return parsed
	}
	parsed, err := patch.ParseMergePatch([]byte(body), ChangeSchema(), nil)
	require.NoError(t, err, body)
	return parsed
}

// FoldSchema models a row with an array column, a scalar column, a
// server-managed timestamp and a server-moved version.
func FoldSchema() *patch.PatchSchema {
	return schema("items", 50, map[string]*patch.PathSchema{
		"/tags": {
			AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpAdd, patch.PatchOpRemove, patch.PatchOpArrayAppend},
			DataType:   core.FieldTypeArray, SQLColumn: "tags",
		},
		"/name": {
			AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpRemove, patch.PatchOpMove, patch.PatchOpCopy, patch.PatchOpTest},
			DataType:   core.FieldTypeString, SQLColumn: "name",
		},
		"/updated_at": {
			AllowedOps: []patch.PatchOp{patch.PatchOpReplace},
			DataType:   core.FieldTypeTimestamp, SQLColumn: "updated_at",
		},
		"/version": {
			AllowedOps: []patch.PatchOp{patch.PatchOpReplace},
			DataType:   core.FieldTypeInt, SQLColumn: "version",
		},
		"/meta/*": {
			AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpAdd, patch.PatchOpArrayAppend},
			DataType:   core.FieldTypeString, SQLColumn: "meta",
		},
	})
}

// MergeSchema is a schema whose /doc is a document column stored as
// storage, taking at most 10 operations.
func MergeSchema(storage core.SQLType) *patch.PatchSchema {
	return schema("docs", 10, map[string]*patch.PathSchema{
		"/doc": {
			AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpRemove},
			DataType:   core.FieldTypeJSON, SQLType: storage, SQLColumn: "doc",
		},
	})
}

// RankedVisit's patchable columns are database/sql's nullable wrappers.
type RankedVisit struct {
	ID     int64         `json:"id"      db:"id"`
	Score  sql.NullInt64 `json:"score"   db:"score"   parse:"patch" patch_ops:"replace,remove,test"`
	SeenAt sql.NullTime  `json:"seen_at" db:"seen_at" parse:"patch" patch_ops:"replace,remove,test"`
}

// RankedSeen is the seen_at a stored RankedVisit holds.
func RankedSeen() time.Time { return time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC) }

// ApplyDims is ApplyRow's struct document, two of its members omitted when
// zero.
type ApplyDims struct {
	W    int    `json:"w"`
	H    int    `json:"h"`
	Note string `json:"note,omitempty"`
	At   *int   `json:"at,omitzero"`
}

// ApplyRow is a row of every kind of column the applier and SQL both
// patch: scalars, a nullable one, an array, an untyped document, a struct
// document and a typed map.
type ApplyRow struct {
	ID     int64             `json:"id" db:"id"`
	Name   string            `json:"name" db:"name" parse:"patch"`
	Note   *string           `json:"note" db:"note" parse:"patch"`
	Rank   int32             `json:"rank" db:"rank" parse:"patch"`
	Tags   []string          `json:"tags" db:"tags" parse:"patch"`
	Meta   map[string]any    `json:"meta" db:"meta" parse:"patch"`
	Dims   ApplyDims         `json:"dims" db:"dims" parse:"patch"`
	Labels map[string]string `json:"labels" db:"labels" parse:"patch"`
}

// MemberPatchSchema is ApplyRow's generated patch schema, with add,
// replace and remove allowed on members of its documents.
func MemberPatchSchema(t testing.TB, gen *core.SchemaGenerator) *patch.PatchSchema {
	t.Helper()
	s, err := patch.GeneratePatchSchema[ApplyRow](gen)
	require.NoError(t, err)
	ops := []patch.PatchOp{patch.PatchOpAdd, patch.PatchOpReplace, patch.PatchOpRemove}
	for path, column := range map[string]string{"/meta/**": "meta", "/dims/*": "dims", "/labels/*": "labels"} {
		require.NoError(t, s.AddPath(path, &patch.PathSchema{AllowedOps: ops, DataType: core.FieldTypeAny, SQLColumn: column}))
	}
	return s
}
