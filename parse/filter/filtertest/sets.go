package filtertest

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// SetSizes are the `$in` / `$nin` set sizes the set cases generate: empty,
// one member, past PostgreSQL's default containment bound
// (filter.DefaultContainmentMaxValues), and an authorized-ID set.
var SetSizes = []int{0, 1, 257, 100_000}

// SetConfig is the parser configuration the set cases parse under: sets
// of up to 100,000 members and filters that size, clock pinned to Now.
func SetConfig() *core.ParserConfig {
	cfg := core.DefaultConfig()
	cfg.Clock = Now
	cfg.MaxArraySize = 100_000
	cfg.MaxInputSize = 16 << 20
	return cfg
}

// setField generates a field's sets: its members are the picks, in order,
// then fillers that match no row, up to the set's size.
type setField struct {
	field string
	picks []setPick
	// present are the rows holding a value; `$nin` of a non-empty set
	// selects them less the rows the set's members match.
	present string
	filler  func(i int) any
}

// setPick is a fixture value and the rows holding it.
type setPick struct {
	value any
	rows  string
}

func setFields() []setField {
	return []setField{
		{field: "name", picks: []setPick{{"beta", "b"}, {"Ωmega", "g"}}, present: "abcefgh",
			filler: func(i int) any { return fmt.Sprintf("set-member-%06d", i) }},
		{field: "count", picks: []setPick{{int64(1), "a"}, {int64(9007199254740993), "e"}}, present: "abcefgh",
			filler: func(i int) any { return int64(1_000_000_000 + i) }},
		{field: "score", picks: []setPick{{2.5, "bh"}, {-1.0, "c"}}, present: "abcefgh",
			filler: func(i int) any { return float64(1_000_000+i) + 0.25 }},
		{field: "amount", picks: []setPick{{"10", "h"}, {"-1", "c"}}, present: "abcefgh",
			filler: func(i int) any { return fmt.Sprintf("%d.125", 1_000_000+i) }},
		{field: "active", picks: []setPick{{false, "be"}}, present: "abcefh",
			filler: func(int) any { return false }},
		{field: "at", picks: []setPick{{"2026-01-02T03:04:07Z", "b"}, {"2026-01-02T03:04:12Z", "g"}}, present: "abcefg",
			filler: func(i int) any { return Base.Add(time.Duration(1_000_000+i) * time.Second).Format(time.RFC3339) }},
		{field: "ref", picks: []setPick{{"0198a3c2-7a1e-7c00-8000-000000000001", "a"}, {"0198a3c2-7a1e-7c00-8000-000000000006", "f"}}, present: "abcefg",
			filler: fillerUUID},
	}
}

func pathSetFields() []setField {
	return []setField{
		{field: "attrs.color", picks: []setPick{{"Red", "g"}, {"blue", "b"}}, present: "abg",
			filler: func(i int) any { return fmt.Sprintf("set-member-%06d", i) }},
		{field: "attrs.size", picks: []setPick{{int64(10), "b"}, {int64(9007199254740993), "e"}}, present: "abeg",
			filler: func(i int) any { return int64(1_000_000_000 + i) }},
		{field: "attrs.ratio", picks: []setPick{{0.1, "e"}, {-1.0, "g"}}, present: "abeg",
			filler: func(i int) any { return float64(1_000_000+i) + 0.25 }},
		{field: "attrs.price", picks: []setPick{{"9.5", "b"}, {"100", "g"}}, present: "abeg",
			filler: func(i int) any { return fmt.Sprintf("%d.125", 1_000_000+i) }},
		{field: "attrs.owner", picks: []setPick{{"0198a3c2-7a1e-7c00-8000-000000000003", "g"}, {"0198a3c2-7a1e-7c00-8000-000000000001", "a"}}, present: "abg",
			filler: fillerUUID},
	}
}

func fillerUUID(i int) any { return fmt.Sprintf("00000000-0000-4000-8000-%012x", i) }

// SetCases are `$in` and `$nin` over every scalar column of Records, one
// case per size in SetSizes. Parse them under SetConfig.
func SetCases() []Case { return slices.Clone(recordSetCases()) }

// PathSetCases are SetCases over PathRecords' typed paths: a string, int,
// float, decimal and UUID member. Parse them under SetConfig.
func PathSetCases() []Case { return slices.Clone(pathSetCases()) }

// The cases are generated once: the largest sets encode to megabytes.
var (
	recordSetCases = sync.OnceValue(func() []Case { return setCases(setFields()) })
	pathSetCases   = sync.OnceValue(func() []Case { return setCases(pathSetFields()) })
)

func setCases(fields []setField) []Case {
	var out []Case
	for _, f := range fields {
		for _, size := range SetSizes {
			members := make([]any, size)
			matched := map[rune]bool{}
			for i := range members {
				if i < len(f.picks) {
					members[i] = f.picks[i].value
					for _, r := range f.picks[i].rows {
						matched[r] = true
					}
					continue
				}
				members[i] = f.filler(i)
			}
			in, nin := "", ""
			for _, r := range "abcdefgh" {
				if matched[r] {
					in += string(r)
				}
				if size == 0 || strings.ContainsRune(f.present, r) && !matched[r] {
					nin += string(r)
				}
			}
			out = append(out,
				setCase(f.field, "$in", members, in),
				setCase(f.field, "$nin", members, nin))
		}
	}
	return out
}

func setCase(field, op string, members []any, want string) Case {
	body, err := json.Marshal(map[string]any{field: map[string]any{op: members}})
	if err != nil {
		panic(fmt.Sprintf("filtertest: marshal %s %s set: %v", field, op, err)) // fixture values always encode
	}
	return Case{Name: fmt.Sprintf("%s %s %d", field, op, len(members)), Filter: string(body), Want: ids(want)}
}

// RunSets evaluates every SetCases case on backend, which evaluates
// against Records.
func RunSets(t *testing.T, name string, backend Backend) {
	schema, err := Schema()
	if err != nil {
		t.Fatalf("filtertest: schema: %v", err)
	}
	run(t, name, filter.NewFilterParser(schema, SetConfig()), SetCases(), backend)
}

// RunPathSets evaluates every PathSetCases case, parsed against schema
// (PathSchema or a copy declaring the backend's storage), on backend, which
// evaluates against PathRecords.
func RunPathSets(t *testing.T, schema *filter.ResourceSchema, backend Backend) {
	run(t, "", filter.NewFilterParser(schema, SetConfig()), PathSetCases(), backend)
}
