//go:build unit || integration

package parsetest

import (
	"encoding/json"
	"maps"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

// KeyedMaps holds maps whose keys encoding/json writes as member names by a
// rule other than the string itself: base-10 integers, and a struct type's
// text methods.
type KeyedMaps struct {
	ID     int64              `json:"id"      db:"id"`
	ByInt  map[int]int        `json:"by_int"  db:"by_int"  parse:"patch"`
	ByUint map[uint8]string   `json:"by_uint" db:"by_uint" parse:"patch"`
	ByAddr map[netip.Addr]int `json:"by_addr" db:"by_addr" parse:"patch"`
}

func KeyedMapsRow() KeyedMaps {
	row := KeyedMaps{
		ID:     1,
		ByInt:  map[int]int{1: 10, 5: 50},
		ByUint: map[uint8]string{7: "seven"},
		ByAddr: map[netip.Addr]int{netip.MustParseAddr("::1"): 1, netip.MustParseAddr("192.0.2.1"): 2},
	}
	return row
}

// KeyedMapMerge is a merge patch body over KeyedMaps.
type KeyedMapMerge struct{ Name, Body string }

// KeyedMapMerges are merge patches that name keys as encoding/json reads
// them, in spellings it writes and spellings it only reads ("01", the long
// form of ::1).
func KeyedMapMerges() []KeyedMapMerge {
	cases := []KeyedMapMerge{
		{"integer keys added and replaced", `{"by_int":{"2":20,"-3":-30,"5":55}}`},
		{"a non-canonical integer names its key", `{"by_int":{"01":11}}`},
		{"null deletes an integer key", `{"by_int":{"1":null,"9":90}}`},
		{"unsigned keys up to the type's range", `{"by_uint":{"7":"SEVEN","255":"max","0":"zero"}}`},
		{"text-method keys", `{"by_addr":{"192.0.2.9":9,"192.0.2.1":null}}`},
		{"a text-method key in another spelling names its key", `{"by_addr":{"0:0:0:0:0:0:0:1":100}}`},
		{"every map at once", `{"by_int":{"5":null},"by_uint":{"8":"eight"},"by_addr":{"::1":null}}`},
	}
	return cases
}

// MergedByEncodingJSON is what a merge patch body makes of row by
// encoding/json's rules: each member's name decodes to a key as Unmarshal
// decodes it into the map, a null member deletes that key (RFC 7396), and
// any other member sets the key to the value Unmarshal decodes.
func MergedByEncodingJSON(t *testing.T, row KeyedMaps, body string) KeyedMaps {
	t.Helper()
	var members struct {
		ByInt  map[int]json.RawMessage        `json:"by_int"`
		ByUint map[uint8]json.RawMessage      `json:"by_uint"`
		ByAddr map[netip.Addr]json.RawMessage `json:"by_addr"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &members))
	row.ByInt = mergeMembers(t, row.ByInt, members.ByInt)
	row.ByUint = mergeMembers(t, row.ByUint, members.ByUint)
	row.ByAddr = mergeMembers(t, row.ByAddr, members.ByAddr)
	return row
}

func mergeMembers[K comparable, V any](t *testing.T, existing map[K]V, members map[K]json.RawMessage) map[K]V {
	t.Helper()
	out := maps.Clone(existing)
	for key, raw := range members {
		if string(raw) == "null" {
			delete(out, key)
			continue
		}
		var v V
		require.NoError(t, json.Unmarshal(raw, &v))
		out[key] = v
	}
	return out
}

// GadgetAttrs has a JSON document whose members a JSON Patch may write at
// any depth.
type GadgetAttrs struct {
	ID    int64          `json:"id"    db:"id"`
	Attrs map[string]any `json:"attrs" db:"attrs" parse:"patch"`
}

func GadgetAttrsResource(t *testing.T, p *parse.Provider) *parse.Resource[GadgetAttrs] {
	t.Helper()
	r, err := parse.NewResource[GadgetAttrs](p, "gadget_attrs", parse.CustomizeSchemas(func(s parse.Schemas) error {
		return s.Patch.AddPath("/attrs/**", &patch.PathSchema{
			AllowedOps: []patch.PatchOp{patch.PatchOpAdd, patch.PatchOpReplace, patch.PatchOpRemove},
			DataType:   core.FieldTypeAny, SQLColumn: "attrs",
		})
	}))
	require.NoError(t, err)
	return r
}

type updateBatchDocument struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

type updateBatchRow struct {
	ID        int64               `json:"id" db:"id" parse:"filter"`
	TenantID  string              `json:"tenant_id" db:"tenant_id"`
	Reference *string             `json:"reference" db:"reference" parse:"filter,patch"`
	Version   int64               `json:"version" db:"version" parse:"filter,server"`
	Meta      updateBatchDocument `json:"meta" db:"meta" parse:"patch"`
	UpdatedAt string              `json:"updated_at" db:"updated_at"`
	DeletedAt *string             `json:"deleted_at" db:"deleted_at"`
	Tags      []string            `json:"tags" db:"-" parse:"patch" relation:"batch_tags,row_id,tag_id,id,tenant_id" indexed:"true"`
}

func UpdateBatchResource(t *testing.T, p *parse.Provider) *parse.Resource[updateBatchRow] {
	t.Helper()
	r, err := parse.NewResource[updateBatchRow](p, "batch_rows", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"), parse.ColumnType("meta", core.SQLJSONB))
	require.NoError(t, err)
	return r
}

func UpdateBatchPatch(t *testing.T, r *parse.Resource[updateBatchRow], raw string) *patch.ParsedPatch {
	t.Helper()
	p, err := r.ParseServerMergePatch([]byte(raw))
	require.NoError(t, err)
	return p
}

type Gadget struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Name      string     `json:"name"       db:"name"       parse:"filter,sort,patch"`
	Rank      int64      `json:"rank"       db:"rank"       parse:"filter,patch"`
	Version   int64      `json:"version"    db:"version"`
	UpdatedAt *time.Time `json:"updated_at" db:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
	Tags      []string   `json:"tags"       db:"-"          parse:"filter,patch" relation:"gadget_tags,gadget_id,tag_id,id,tenant_id" indexed:"true"`
}

func Gadgets(t *testing.T, p *parse.Provider) *parse.Resource[Gadget] {
	t.Helper()
	r, err := parse.NewResource[Gadget](p, "gadgets", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	require.NoError(t, err)
	return r
}

type Note struct {
	ID        string         `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string         `json:"tenant_id"  db:"tenant_id"`
	Title     *string        `json:"title"      db:"title"      parse:"filter,patch"`
	Rank      int64          `json:"rank"       db:"rank"       parse:"filter,patch"`
	Labels    []string       `json:"labels"     db:"labels"     parse:"patch"`
	Attrs     map[string]any `json:"attrs"      db:"attrs"      parse:"patch"`
	Version   int64          `json:"version"    db:"version"`
	UpdatedAt *time.Time     `json:"updated_at" db:"updated_at"`
	DeletedAt *time.Time     `json:"deleted_at" db:"deleted_at"`
	Tags      []string       `json:"tags"       db:"-"          parse:"filter,patch" relation:"note_tags,note_id,tag_id,id,tenant_id" indexed:"true"`
}

// noteColumns are the columns the statements return; labels and attrs are
// compared by the database and read back on their own.
func noteColumns() []string {
	columns := []string{"id", "tenant_id", "title", "rank", "version", "updated_at", "deleted_at"}
	return columns
}

func Notes(t *testing.T, p *parse.Provider) *parse.Resource[Note] {
	t.Helper()
	r, err := parse.NewResource[Note](p, "notes", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"),
		parse.ReadColumns(noteColumns()...))
	require.NoError(t, err)
	return r
}

func SkipAt() time.Time {
	return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
}

// SkipOptions are a SkipUnchanged update's that stamps and counts.
func SkipOptions() []parse.UpdateOption {
	opts := []parse.UpdateOption{parse.Stamp("updated_at", SkipAt()), parse.Increment("version"), parse.SkipUnchanged()}
	return opts
}

// SkipUpdate builds body, a merge patch, for target in tenant t1 with opts,
// SkipOptions when there are none.
func SkipUpdate(t *testing.T, r *parse.Resource[Note], body string, target parse.Target, opts ...parse.UpdateOption) *parse.UpdatePlan {
	t.Helper()
	if len(opts) == 0 {
		opts = SkipOptions()
	}
	p, err := r.ParseMergePatch([]byte(body))
	require.NoError(t, err)
	plan, err := r.BuildUpdate(p, target, parse.Scope{Tenant: "t1"}, opts...)
	require.NoError(t, err)
	return plan
}
