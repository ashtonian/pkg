package kongprovider

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/config"
)

type usageDB struct {
	Host     string `koanf:"host" default:"localhost" desc:"Database host"`
	Password string `koanf:"password" kong:"-" desc:"Database password"`
}

type usageConfig struct {
	Port    int               `koanf:"port" default:"8080" short:"p" desc:"Listen port"`
	Metrics bool              `koanf:"metrics" default:"true" negatable:"" desc:"Serve metrics"`
	Verbose bool              `koanf:"verbose" negatable:"quiet" desc:"Log more"`
	Debug   bool              `koanf:"debug" hidden:"" desc:"Debug endpoints"`
	Labels  map[string]string `koanf:"labels" kong:"-" desc:"Labels"`
	DB      usageDB           `koanf:"db" embed:"" prefix:"db-"`
}

type usageCLI struct {
	Config string `type:"path" help:"Config file."`
	Serve  struct {
		App usageConfig `embed:""`
	} `cmd:""`
}

func TestUsageFlags(t *testing.T) {
	var cli usageCLI
	parser, err := kong.New(&cli)
	require.NoError(t, err)
	flags, err := UsageFlags(parser, &cli.Serve.App)
	require.NoError(t, err)
	var md bytes.Buffer
	require.NoError(t, config.PrintUsage[usageConfig]("APP", config.WithWriter(&md), config.WithFormat(config.FormatMarkdown), flags))

	rows := map[string]string{}
	for line := range strings.Lines(md.String()) {
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), " | ")
		if len(cells) > 2 {
			rows[strings.TrimSpace(cells[0])] = strings.TrimSpace(cells[2])
		}
	}
	assert.Equal(t, "Flag", rows["Key"])
	tests := []struct{ key, flag string }{
		{"port", "-p, --port"},
		{"metrics", "--[no-]metrics"},
		{"verbose", "--verbose/quiet"},
		{"debug", ""},     // hidden
		{"labels", ""},    // kong:"-"
		{"labels[*]", ""}, // a map entry's row
		{"db.host", "--db-host"},
		{"db.password", ""}, // kong:"-"
	}
	for _, tt := range tests {
		got, ok := rows[tt.key]
		assert.True(t, ok, "no row for %s", tt.key)
		assert.Equal(t, tt.flag, got, tt.key)
	}
	assert.NotContains(t, md.String(), "--config", "the CLI's own flags are not the config's")
}

// A flag is named as Kong's own help names it, short name and negation
// included.
func TestUsageFlags_NamesMatchKongHelp(t *testing.T) {
	var cli usageCLI
	var help bytes.Buffer
	parser, err := kong.New(&cli, kong.Writers(&help, &help), kong.Exit(func(int) {}))
	require.NoError(t, err)
	_, _ = parser.Parse([]string{"serve", "--help"})
	fields, err := fieldsOfFlags(&cli.Serve.App, allFlags(parser.Model.Node))
	require.NoError(t, err)
	for fl := range fields {
		if !fl.Hidden {
			assert.Contains(t, help.String(), flagUsage(fl), fl.Name)
		}
	}
}

func TestUsageFlags_Errors(t *testing.T) {
	var cli usageCLI
	parser, err := kong.New(&cli)
	require.NoError(t, err)
	var outside usageConfig
	tests := []struct {
		name string
		call func() error
	}{
		{"nil parser", func() error { _, err := UsageFlags[usageConfig](nil, &cli.Serve.App); return err }},
		{"nil config", func() error { _, err := UsageFlags[usageConfig](parser, nil); return err }},
		{"config outside the CLI", func() error { _, err := UsageFlags(parser, &outside); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Error(t, tt.call())
		})
	}
}
