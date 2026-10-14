package kongprovider

import (
	"testing"

	"github.com/alecthomas/kong"
	"github.com/ashtonian/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A list flag is forwarded as the list Kong parsed, whatever the field's
// sep tag: the config package splits only strings.
func TestTags_ListFlagsForwardAsParsed(t *testing.T) {
	type cfg struct {
		Hosts []string `koanf:"hosts" sep:";"`
	}
	var cli cfg
	parser, err := kong.New(&cli, HelpFromConfig(&cli, "APP"))
	require.NoError(t, err)
	kctx, err := parser.Parse([]string{"--hosts=a;b"})
	require.NoError(t, err)
	got, err := config.Load[cfg](t.Context(), WithOverridesFromStruct(kctx, &cli))
	require.NoError(t, err)
	assert.Equal(t, cli.Hosts, got.Hosts)
}

// A value Kong takes from an env tag is a flag default, not a typed flag, so
// it is not forwarded as an override.
func TestTags_KongEnvironmentIsNotAnOverride(t *testing.T) {
	type cfg struct {
		Port int `koanf:"port" env:"REVIEW_PORT"`
	}
	t.Setenv("REVIEW_PORT", "80")
	var cli cfg
	parser, err := kong.New(&cli, HelpFromConfig(&cli, "APP"))
	require.NoError(t, err)
	kctx, err := parser.Parse(nil)
	require.NoError(t, err)
	assert.Equal(t, 80, cli.Port)
	got, err := config.Load[cfg](t.Context(), config.WithDefaults(map[string]any{"port": 90}), WithOverridesFromStruct(kctx, &cli))
	require.NoError(t, err)
	assert.Equal(t, 90, got.Port)
}

func TestOverrides_UnrelatedAnonymousConfigDoesNotQuoteTags(t *testing.T) {
	var cli struct{ Port int }
	parser, err := kong.New(&cli)
	require.NoError(t, err)
	kctx, err := parser.Parse(nil)
	require.NoError(t, err)
	cfg := struct {
		Password string `koanf:"password" default:"FAKE_CREDENTIAL"`
	}{}
	_, err = config.Load[struct{ Port int }](t.Context(), WithOverridesFromStruct(kctx, &cfg))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "FAKE_CREDENTIAL")
}
