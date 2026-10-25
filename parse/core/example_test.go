//go:build unit

package core_test

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/ashtonian/pkg/parse/core"
)

// Example is the README Quick Start: shared limits, exact numbers, JSON
// Pointer paths and Go type classification.
func Example() {
	cfg := core.DefaultConfig()
	core.WithMaxDepth(3)(cfg)
	fmt.Println("depth", cfg.MaxDepth, "array", cfg.MaxArraySize)

	// Request numbers normalize exactly against the field's Go kind.
	n, err := core.NormalizeInt(json.Number("9007199254740993"), reflect.Int64)
	fmt.Println(n, err)
	_, err = core.NormalizeUint(json.Number("300"), reflect.Uint8)
	fmt.Println(err)

	tokens, err := core.ParsePointer("/labels/a~1b")
	fmt.Println(tokens, err, core.FormatPointer("labels", "a/b"))

	fmt.Println(core.TypeOf(reflect.TypeFor[[]string]()).Type, core.TypeOf(reflect.TypeFor[map[string]any]()).Type)
	// Output:
	// depth 3 array 100
	// 9007199254740993 <nil>
	// 300 overflows uint8
	// [labels a/b] <nil> /labels/a~1b
	// array json
}
