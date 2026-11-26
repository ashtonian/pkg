//go:build goexperiment.jsonv2

package gowalker_test

import (
	"encoding/json/jsontext"
	"fmt"
	"testing"

	"github.com/ashtonian/pkg/gowalker"
)

// streamStampA and streamStampB write themselves through encoding/json/v2's
// MarshalJSONTo, which encoding/json calls too, and streamStampC reads
// itself through UnmarshalJSONFrom. A struct embedding all three under tag
// names inherits no MarshalJSONTo, the two colliding, and encoding/json
// drops all three members.
type streamStampA struct{ At int }

func (streamStampA) MarshalJSONTo(enc *jsontext.Encoder) error {
	return enc.WriteToken(jsontext.String("a"))
}

type streamStampB struct{ At int }

func (streamStampB) MarshalJSONTo(enc *jsontext.Encoder) error {
	return enc.WriteToken(jsontext.String("b"))
}

type streamStampC struct{ At int }

func (*streamStampC) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	return dec.SkipValue()
}

type jsonStreamedPrivately struct {
	streamStampA `json:"a"`
	streamStampB `json:"b"`
	streamStampC `json:"c"`
	N            int `json:"n"`
}

func TestJSONNaming_PrivateEmbedsWithStreamingMethods(t *testing.T) {
	for _, nested := range []bool{false, true} {
		var options []gowalker.TagOption
		if nested {
			options = append(options, gowalker.NestedFields())
		}
		label := fmt.Sprintf("nested=%t", nested)
		checkJSONMembers(t, engine(t, gowalker.WithJSONFields("json", options...)), label, jsonStreamedPrivately{N: 7})
	}
}
