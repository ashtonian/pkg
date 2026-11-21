//go:build unit

package filtertest

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/stretchr/testify/assert"
)

func TestCheck(t *testing.T) {
	geo := Case{Want: ids("ab"), Unsupported: []string{"memory"}, UnsupportedErr: "E2115"}
	refusal := &core.ParseError{Kind: core.ParseErrorKindFilter, Code: "E2115", Message: "geo operator '$near' is not supported"}
	tests := []struct {
		name        string
		c           Case
		unsupported bool
		got         []string
		err         error
		conforms    bool
	}{
		{"an unsupported backend's refusal", geo, true, nil, refusal, true},
		{"an unsupported backend's wrapped refusal", geo, true, nil, fmt.Errorf("building where: %w", refusal), true},
		{"an unsupported backend's plain error", geo, true, nil, errors.New("sql: database is closed"), false},
		{"an unsupported backend's refusal with another code", geo, true, nil, &core.ParseError{Code: "E2072"}, false},
		{"an unsupported backend's rows", geo, true, ids("ab"), nil, false},
		{"the wanted rows in any order", geo, false, []string{"b", "a"}, nil, true},
		{"no rows for an empty want", Case{Want: ids("")}, false, nil, nil, true},
		{"other rows", geo, false, ids("a"), nil, false},
		{"a supported backend's refusal", geo, false, nil, refusal, false},
		{"Unsupported backends without their code", Case{Want: ids("ab"), Unsupported: []string{"memory"}}, false, ids("ab"), nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := check(tt.c, tt.unsupported, tt.got, tt.err)
			if tt.conforms {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}
