//go:build unit

package filter

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

// The filter conformance corpus (parse/testdata/filter_conformance.json,
// emitted by parse's TestEmitFilterConformance) holds, for every operator of
// the grammar and every type a field may filter on, a case the server
// accepts and a case it refuses. Adding an operator or a filterable type
// fails this test until the corpus, and so the clients held to it, covers it.
func TestFilterCorpus_CoversEveryOperatorAndType(t *testing.T) {
	data, err := os.ReadFile("../testdata/filter_conformance.json")
	require.NoError(t, err)
	var corpus struct {
		Schemas map[string]struct {
			Filter *corpusSchema `json:"filter"`
		} `json:"schemas"`
		Cases []struct {
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
			Valid bool           `json:"valid"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(data, &corpus))
	entities := corpus.Schemas["conf_entities"].Filter
	require.NotNil(t, entities)
	targets := func(name string) *corpusSchema { return corpus.Schemas[name].Filter }

	verdict := func(valid bool) string {
		if valid {
			return "valid"
		}
		return "invalid"
	}
	ops := map[string][]string{}
	types := map[string][]string{}
	for _, c := range corpus.Cases {
		w := coverageWalk{targets: targets, ops: map[FilterOperator]bool{}, types: map[core.FieldType]bool{}}
		w.filter(entities, c.Input)
		for op := range w.ops {
			ops[string(op)+" "+verdict(c.Valid)] = append(ops[string(op)+" "+verdict(c.Valid)], c.Name)
		}
		for ft := range w.types {
			types[string(ft)+" "+verdict(c.Valid)] = append(types[string(ft)+" "+verdict(c.Valid)], c.Name)
		}
	}
	for _, d := range operatorTable {
		for _, v := range []string{"valid", "invalid"} {
			assert.NotEmpty(t, ops[string(d.op)+" "+v], "no %s case uses $%s", v, d.op)
		}
	}
	for ft := range typeOperators {
		for _, v := range []string{"valid", "invalid"} {
			assert.NotEmpty(t, types[string(ft)+" "+v], "no %s case filters a %s field", v, ft)
		}
	}
}

// corpusSchema is the filter section of a served schema document, as much
// of it as the walk reads.
type corpusSchema struct {
	Fields map[string]*corpusField `json:"fields"`
}

type corpusField struct {
	DataType core.FieldType `json:"data_type"`
	Relation *struct {
		Target string `json:"target"`
	} `json:"relation"`
	Elements map[string]*corpusField `json:"elements"`
	Element  *corpusField            `json:"element"`
}

// coverageWalk records the operators a filter document names and the types
// of the fields it filters, reading it as the grammar does: a field's value
// is an operator object when its keys all start with "$", and otherwise an
// equality.
type coverageWalk struct {
	targets func(name string) *corpusSchema
	ops     map[FilterOperator]bool
	types   map[core.FieldType]bool
}

func (w coverageWalk) filter(s *corpusSchema, doc map[string]any) {
	for key, v := range doc {
		if op, ok := strings.CutPrefix(key, "$"); ok {
			w.ops[FilterOperator(op)] = true
			w.logical(FilterOperator(op), v, func(sub map[string]any) { w.filter(s, sub) })
			continue
		}
		if f := s.Fields[key]; f != nil {
			w.field(f, v)
		}
	}
}

// logical walks the operands of $and, $or and $not with walk.
func (w coverageWalk) logical(op FilterOperator, v any, walk func(map[string]any)) {
	switch op {
	case OpAnd, OpOr:
		if list, ok := v.([]any); ok {
			for _, el := range list {
				if sub, ok := el.(map[string]any); ok {
					walk(sub)
				}
			}
		}
	case OpNot:
		if sub, ok := v.(map[string]any); ok {
			walk(sub)
		}
	}
}

func (w coverageWalk) field(f *corpusField, v any) {
	w.types[f.DataType] = true
	obj, ok := v.(map[string]any)
	if !ok || !operatorObject(obj) {
		w.ops[OpEq] = true
		return
	}
	for key, arg := range obj {
		op := FilterOperator(strings.TrimPrefix(key, "$"))
		w.ops[op] = true
		switch op {
		case OpSome, OpNone, OpIs, OpIsNot:
			sub, ok := arg.(map[string]any)
			switch {
			case !ok:
			case f.Relation != nil:
				if target := w.targets(f.Relation.Target); target != nil {
					w.filter(target, sub)
				}
			case f.Elements != nil:
				w.filter(&corpusSchema{Fields: f.Elements}, sub)
			case f.Element != nil:
				w.condition(f.Element, sub)
			}
		}
	}
}

// condition walks a scalar array element's condition: operators applied to
// the element, combined by $and, $or and $not.
func (w coverageWalk) condition(el *corpusField, cond map[string]any) {
	w.types[el.DataType] = true
	for key, arg := range cond {
		op := FilterOperator(strings.TrimPrefix(key, "$"))
		w.ops[op] = true
		w.logical(op, arg, func(sub map[string]any) { w.condition(el, sub) })
	}
}

// operatorObject reports whether obj's keys are all operators.
func operatorObject(obj map[string]any) bool {
	for k := range obj {
		if !strings.HasPrefix(k, "$") {
			return false
		}
	}
	return len(obj) > 0
}
