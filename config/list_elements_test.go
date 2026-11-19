package config

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListElements(t *testing.T) {
	tests := []struct {
		name  string
		parts []string
		want  []string
	}{
		{"trimmed", []string{"a", " b", "c "}, []string{"a", "b", "c"}},
		{"empty dropped", []string{"a", "", "  ", "b", ""}, []string{"a", "b"}},
		{"inner space kept", []string{" hello world "}, []string{"hello world"}},
		{"tabs and newlines", []string{"\ta\n", "b\r\n"}, []string{"a", "b"}},
		{"nothing", nil, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ListElements(tt.parts))
		})
	}
}

type listCfg struct {
	Hosts  []string          `koanf:"hosts" default:"a, b,"`
	Ports  []int             `koanf:"ports"`
	Paths  []string          `koanf:"paths" sep:";"`
	Whole  []string          `koanf:"whole" sep:"none"`
	Labels map[string]string `koanf:"labels"`
	Items  []struct {
		Tags []string `koanf:"tags" default:" x , y "`
	} `koanf:"items"`
}

// Every source that sets a list from a string trims its elements and drops
// the empty ones; sep:"none" keeps the string as written; a list a file
// writes as a list keeps its elements.
func TestListElements_EverySource(t *testing.T) {
	tests := []struct {
		name    string
		file    string
		environ []string
		check   func(t *testing.T, got *listCfg)
	}{
		{
			name:  "tag default",
			check: func(t *testing.T, got *listCfg) { assert.Equal(t, []string{"a", "b"}, got.Hosts) },
		},
		{
			name:    "variable",
			environ: []string{"APP_HOSTS= c ,d", "APP_PORTS=80, 443,"},
			check: func(t *testing.T, got *listCfg) {
				assert.Equal(t, []string{"c", "d"}, got.Hosts)
				assert.Equal(t, []int{80, 443}, got.Ports)
			},
		},
		{
			name:    "sep tag",
			environ: []string{"APP_PATHS=/a ; /b;;"},
			check:   func(t *testing.T, got *listCfg) { assert.Equal(t, []string{"/a", "/b"}, got.Paths) },
		},
		{
			name:    "sep none",
			environ: []string{"APP_WHOLE= a, b "},
			check:   func(t *testing.T, got *listCfg) { assert.Equal(t, []string{" a, b "}, got.Whole) },
		},
		{
			name:    "map entries",
			environ: []string{"APP_LABELS=team=core, tier=1,"},
			check: func(t *testing.T, got *listCfg) {
				assert.Equal(t, map[string]string{"team": "core", "tier": "1"}, got.Labels)
			},
		},
		{
			name: "file string",
			file: "hosts: 'e, f'\n",
			check: func(t *testing.T, got *listCfg) {
				assert.Equal(t, []string{"e", "f"}, got.Hosts)
			},
		},
		{
			name: "file list kept as written",
			file: "hosts: [' e', 'f ']\n",
			check: func(t *testing.T, got *listCfg) {
				assert.Equal(t, []string{" e", "f "}, got.Hosts)
			},
		},
		{
			name: "element default",
			file: "items:\n  - {}\n",
			check: func(t *testing.T, got *listCfg) {
				require.Len(t, got.Items, 1)
				assert.Equal(t, []string{"x", "y"}, got.Items[0].Tags)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := []Option{WithEnvPrefix("APP"), WithEnviron(func() []string { return tt.environ })}
			if tt.file != "" {
				file := filepath.Join(t.TempDir(), "config.yaml")
				writeFile(t, file, tt.file)
				opts = append(opts, WithFile(file))
			}
			got, err := Load[listCfg](t.Context(), opts...)
			require.NoError(t, err)
			tt.check(t, got)
		})
	}
}

// PrintEnv writes no variable for a list whose elements splitting would
// change: an empty element, or one with surrounding white space.
func TestPrintEnv_ListElementsSplittingWouldChange(t *testing.T) {
	tests := []struct {
		name  string
		hosts []string
		want  string
	}{
		{"plain", []string{"a", "b"}, "export APP_HOSTS='a,b'"},
		{"surrounding space", []string{" a", "b"}, "# hosts: its list cannot be represented by a variable"},
		{"empty element", []string{"a", ""}, "# hosts: its list cannot be represented by a variable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			require.NoError(t, PrintEnv(&listCfg{Hosts: tt.hosts}, "APP", WithWriter(&out)))
			assert.Contains(t, out.String(), tt.want)
		})
	}
}
