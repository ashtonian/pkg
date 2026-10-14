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

// With HelpFromConfig, Kong does not read a config field's env tag: a
// variable meant for something else (a Kubernetes service link) does not
// fail the command line, the config package reads the variable with its own
// precedence, and the help still names it.
func TestHelpFromConfig_LeavesEnvTagsToTheConfigPackage(t *testing.T) {
	type cfg struct {
		RedisPort int `koanf:"redis_port" env:"REDIS_PORT"`
		Port      int `koanf:"port" env:"PORT" default:"8080"`
	}
	t.Setenv("REDIS_PORT", "tcp://10.0.0.1:6379")
	t.Setenv("PORT", "80")
	var cli cfg
	parser, err := kong.New(&cli, HelpFromConfig(&cli, "APP"))
	require.NoError(t, err)
	kctx, err := parser.Parse(nil)
	require.NoError(t, err, "Kong must not parse REDIS_PORT")
	assert.Equal(t, 8080, cli.Port, "Kong took the default, not PORT")
	for _, fl := range parser.Model.Flags {
		if fl.Name == "port" {
			assert.Empty(t, fl.Tag.Envs)
			assert.Contains(t, fl.Help, "env: APP_PORT, PORT")
		}
	}

	got, err := config.Load[cfg](t.Context(), config.WithEnvPrefix("APP"),
		config.WithEnviron(func() []string { return []string{"PORT=80", "APP_REDIS_PORT=6379"} }),
		WithOverridesFromStruct(kctx, &cli))
	require.NoError(t, err)
	assert.Equal(t, 80, got.Port)
	assert.Equal(t, 6379, got.RedisPort)
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
