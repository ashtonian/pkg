//go:build unit

package core_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ashtonian/pkg/parse/core"
)

func TestParserConfig_ListBodyLimit(t *testing.T) {
	tests := []struct {
		name string
		opts []core.Option
		want int
	}{
		{"the default input size with headroom", nil, 64<<10 + 16<<10},
		{"a raised input size with headroom", []core.Option{core.WithMaxInputSize(1 << 20)}, 1<<20 + 16<<10},
		{"an unlimited input size", []core.Option{core.WithMaxInputSize(0)}, 1 << 20},
		{"a negative input size, which is unlimited", []core.Option{core.WithMaxInputSize(-1)}, 1 << 20},
		{"an input size the headroom would overflow", []core.Option{core.WithMaxInputSize(math.MaxInt - 1)}, math.MaxInt},
		{"a set list body size", []core.Option{core.WithMaxListBodySize(4096)}, 4096},
		{"a set list body size beside an unlimited input size", []core.Option{core.WithMaxInputSize(0), core.WithMaxListBodySize(4096)}, 4096},
		{"a zero list body size, which derives", []core.Option{core.WithMaxListBodySize(0)}, 64<<10 + 16<<10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := core.DefaultConfig()
			for _, opt := range tt.opts {
				opt(cfg)
			}
			assert.Equal(t, tt.want, cfg.ListBodyLimit())
		})
	}
}
