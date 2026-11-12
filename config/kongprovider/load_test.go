package kongprovider

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/config"
)

type DBSection struct {
	Host     string `koanf:"host" default:"localhost" desc:"Database host"`
	Password string `koanf:"password" kong:"-"`
}

// Validate is a section's rule. The config package does not call it — only
// a root config's — so Kong is the only caller.
func (d *DBSection) Validate() error {
	if d.Host == "unreachable" {
		return errors.New("db.host is unreachable")
	}
	return nil
}

type ServeConfig struct {
	Port    int            `koanf:"port" default:"8080" short:"p" aliases:"listen-port" desc:"HTTP listen port"`
	Metrics bool           `koanf:"metrics" default:"true" negatable:""`
	Level   string         `koanf:"level" default:"info"`
	Tags    []string       `koanf:"tags"`
	DB      DBSection         `koanf:"db" embed:"" prefix:"db-"`
	Limits  map[string]int `koanf:"limits" kong:"-"`
}

// Validate is the cross-field rule of the issue: it refuses the config that
// Kong's defaults alone make.
func (c *ServeConfig) Validate() error {
	if c.Port == 8080 && c.DB.Host == "localhost" {
		return errors.New("refusing the all-default config")
	}
	return nil
}

type serveCmd struct {
	ServeConfig `embed:""`

	ran *ServeConfig // what Run received
}

func (c *serveCmd) Run(cfg *ServeConfig) error {
	c.ran = cfg
	return nil
}

type AdminConfig struct {
	Listen string `koanf:"listen" default:":9090" desc:"Admin listen address"`
	Token  string `koanf:"token" validate:"required" kong:"-"`
}

type adminCmd struct {
	AdminConfig `embed:""`

	ran *AdminConfig
}

func (c *adminCmd) Run(cfg *AdminConfig) error {
	c.ran = cfg
	return nil
}

type templateCmd struct{}

func (templateCmd) Run() error { return nil }

type loadCLI struct {
	Config   string      `type:"path" help:"Config file."`
	Serve    serveCmd    `cmd:""`
	Admin    adminCmd    `cmd:""`
	Template templateCmd `cmd:""`
}

// parseLoadCLI builds the CLI with a Load option per command and parses
// args. environ is the environment the loads read.
func parseLoadCLI(t *testing.T, environ []string, args ...string) (*loadCLI, *kong.Context, error) {
	t.Helper()
	var cli loadCLI
	opts := func(*kong.Context) ([]config.Option, error) {
		out := []config.Option{config.WithEnviron(func() []string { return environ })}
		if cli.Config != "" {
			out = append(out, config.WithRequiredFile(cli.Config))
		}
		return out, nil
	}
	parser, err := kong.New(&cli,
		Load(t.Context(), &cli.Serve.ServeConfig, []string{"APP"}, opts),
		Load(t.Context(), &cli.Admin.AdminConfig, []string{"ADMIN"}, opts),
		kong.Exit(func(int) { t.Fatal("kong exited") }),
	)
	require.NoError(t, err)
	kctx, err := parser.Parse(args)
	return &cli, kctx, err
}

// The issue's repro: an environment-only invocation passes the config's
// Validate method, and Run's receiver holds the environment's values.
func TestLoad_EnvironmentOnlyInvocation(t *testing.T) {
	cli, kctx, err := parseLoadCLI(t, []string{"APP_PORT=9000", "APP_DB_HOST=db.internal"}, "serve")
	require.NoError(t, err, "Kong must validate the loaded config, not its defaults")
	assert.Equal(t, 9000, cli.Serve.Port)
	assert.Equal(t, "db.internal", cli.Serve.DB.Host)

	require.NoError(t, kctx.Run())
	assert.Same(t, &cli.Serve.ServeConfig, cli.Serve.ran, "Run's *T parameter is the config the receiver holds")
}

// What Kong's defaults and typed flags alone would pass, the loaded config
// fails: the error is the load's, not a usage error.
func TestLoad_InvalidLoadedConfigFailsTheLoad(t *testing.T) {
	_, _, err := parseLoadCLI(t, []string{"APP_PORT=8080"}, "serve", "--db-host", "localhost")
	require.ErrorIs(t, err, config.ErrValidation)
	assert.Contains(t, err.Error(), "refusing the all-default config")

	var stderr bytes.Buffer
	exit := -1
	var cli loadCLI
	parser, err := kong.New(&cli,
		Load(t.Context(), &cli.Serve.ServeConfig, []string{"APP"}, func(*kong.Context) ([]config.Option, error) {
			return []config.Option{config.WithEnviron(func() []string { return nil })}, nil
		}),
		kong.Writers(&bytes.Buffer{}, &stderr),
		kong.Exit(func(code int) { exit = code }),
	)
	require.NoError(t, err)
	_, err = parser.Parse([]string{"serve"})
	parser.FatalIfErrorf(err)
	assert.Equal(t, 1, exit, "a failed load is not Kong's usage error (80)")
	assert.Contains(t, stderr.String(), "refusing the all-default config")
}

// The Validate method of an embedded section — which only Kong calls — sees
// the loaded section too, and its error is Kong's validation error.
func TestLoad_SectionValidateSeesTheLoadedSection(t *testing.T) {
	_, _, err := parseLoadCLI(t, []string{"APP_DB_HOST=unreachable", "APP_PORT=9000"}, "serve")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "db.host is unreachable")
	assert.NotErrorIs(t, err, config.ErrValidation, "Kong's validator, not the load, refused it")

	_, _, err = parseLoadCLI(t, []string{"APP_PORT=9000"}, "serve", "--db-host", "db.internal")
	require.NoError(t, err)
}

// After Parse the config equals what config.Load returns for the same
// sources, fields with no flag included.
func TestLoad_ConfigEqualsTheLoadedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("level: warn\nlimits:\n  upload: 5\ntags: [a, b]\n"), 0o600))
	environ := []string{"APP_PORT=9000", "APP_DB_PASSWORD=s3cret"}
	cli, _, err := parseLoadCLI(t, environ, "--config", path, "serve", "--db-host", "db.internal", "--no-metrics")
	require.NoError(t, err)

	want, err := config.Load[ServeConfig](t.Context(),
		config.WithRequiredFile(path),
		config.WithEnvPrefix("APP"),
		config.WithEnviron(func() []string { return environ }),
		config.WithOverrideProvider(confmap.Provider(map[string]any{"db.host": "db.internal", "metrics": false}, "."), nil),
	)
	require.NoError(t, err)
	assert.Equal(t, *want, cli.Serve.ServeConfig)
	assert.Equal(t, "s3cret", cli.Serve.DB.Password, "a field with no flag")
	assert.Equal(t, map[string]int{"upload": 5}, cli.Serve.Limits)
}

// Typed flags win over the environment in every spelling Kong accepts.
func TestLoad_TypedFlagsInEverySpelling(t *testing.T) {
	environ := []string{"APP_PORT=7000", "APP_METRICS=true", "APP_DB_HOST=env-host"}
	for _, tt := range []struct {
		name  string
		args  []string
		check func(t *testing.T, c *ServeConfig)
	}{
		{"long", []string{"--port", "9001"}, func(t *testing.T, c *ServeConfig) { assert.Equal(t, 9001, c.Port) }},
		{"long with equals", []string{"--port=9002"}, func(t *testing.T, c *ServeConfig) { assert.Equal(t, 9002, c.Port) }},
		{"short", []string{"-p", "9003"}, func(t *testing.T, c *ServeConfig) { assert.Equal(t, 9003, c.Port) }},
		{"alias", []string{"--listen-port", "9004"}, func(t *testing.T, c *ServeConfig) { assert.Equal(t, 9004, c.Port) }},
		{"negated", []string{"--no-metrics"}, func(t *testing.T, c *ServeConfig) { assert.False(t, c.Metrics) }},
		{"nested", []string{"--db-host=flag-host"}, func(t *testing.T, c *ServeConfig) { assert.Equal(t, "flag-host", c.DB.Host) }},
		{"list", []string{"--tags", "x,y"}, func(t *testing.T, c *ServeConfig) { assert.Equal(t, []string{"x", "y"}, c.Tags) }},
		{"none", nil, func(t *testing.T, c *ServeConfig) {
			assert.Equal(t, 7000, c.Port, "an untyped flag does not mask the environment")
			assert.True(t, c.Metrics)
			assert.Equal(t, "env-host", c.DB.Host)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cli, _, err := parseLoadCLI(t, environ, append([]string{"serve"}, tt.args...)...)
			require.NoError(t, err)
			tt.check(t, &cli.Serve.ServeConfig)
		})
	}
}

// opts reads the CLI's own flags — a config file's path — directly.
func TestLoad_ConfigPathFlag(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("port: 9100\ndb:\n  host: file-host\n"), 0o600))
	cli, _, err := parseLoadCLI(t, nil, "serve", "--config="+path)
	require.NoError(t, err)
	assert.Equal(t, path, cli.Config)
	assert.Equal(t, 9100, cli.Serve.Port)
	assert.Equal(t, "file-host", cli.Serve.DB.Host)
}

// Only the selected command's config loads: a sibling's would fail here —
// serve's on its Validate, admin's on its required token — and a command
// holding no config loads none.
func TestLoad_SiblingCommands(t *testing.T) {
	cli, kctx, err := parseLoadCLI(t, []string{"ADMIN_TOKEN=t", "APP_PORT=9000"}, "admin", "--listen", ":9999")
	require.NoError(t, err)
	assert.Equal(t, AdminConfig{Listen: ":9999", Token: "t"}, cli.Admin.AdminConfig)
	assert.Equal(t, 8080, cli.Serve.Port, "serve's config holds Kong's default, not APP_PORT: it was not loaded")
	require.NoError(t, kctx.Run())
	assert.Same(t, &cli.Admin.AdminConfig, cli.Admin.ran)

	cli, kctx, err = parseLoadCLI(t, []string{"APP_PORT=9000"}, "serve")
	require.NoError(t, err, "admin's missing token does not matter to serve")
	require.NoError(t, kctx.Run())
	assert.Same(t, &cli.Serve.ServeConfig, cli.Serve.ran)

	_, kctx, err = parseLoadCLI(t, nil, "template")
	require.NoError(t, err, "neither config loads, so neither is validated")
	require.NoError(t, kctx.Run())
}

// A post-load hook that normalizes a typed flag's value: Kong's validators
// and the config see the normalized value, not the one typed.
func TestLoad_KongValidatesThePostLoadValue(t *testing.T) {
	var cli struct {
		Serve struct {
			normalized `embed:""`
		} `cmd:""`
	}
	parser, err := kong.New(&cli, Load(t.Context(), &cli.Serve.normalized, nil, func(*kong.Context) ([]config.Option, error) {
		return []config.Option{config.WithPostLoad(func(_ context.Context, c *normalized) error {
			c.Level = strings.ToLower(c.Level)
			return nil
		})}, nil
	}))
	require.NoError(t, err)
	_, err = parser.Parse([]string{"serve", "--level", "DEBUG"})
	require.NoError(t, err, "Kong must not validate the value as typed")
	assert.Equal(t, "debug", cli.Serve.Level)
}

type normalized struct {
	Level string `koanf:"level" default:"info"`
}

func (n *normalized) Validate() error {
	if n.Level != strings.ToLower(n.Level) {
		return errors.New("level is not normalized")
	}
	return nil
}

// A section behind an embedded pointer: Kong allocates it and its flags
// write through that allocation, which the load keeps.
func TestLoad_PointerSectionKeepsKongsAllocation(t *testing.T) {
	type section struct {
		Host string `koanf:"host" default:"localhost"`
		Port int    `koanf:"port" kong:"-"`
	}
	type withPointer struct {
		DB *section `koanf:"db" embed:"" prefix:"db-"`
	}
	var cli withPointer
	parser, err := kong.New(&cli, Load(t.Context(), &cli, nil, func(*kong.Context) ([]config.Option, error) {
		return []config.Option{config.WithDefaults(map[string]any{"db.port": 5432})}, nil
	}))
	require.NoError(t, err)
	allocated := cli.DB
	require.NotNil(t, allocated)

	_, err = parser.Parse([]string{"--db-host", "h1"})
	require.NoError(t, err)
	assert.Same(t, allocated, cli.DB)
	assert.Equal(t, section{Host: "h1", Port: 5432}, *cli.DB)

	_, err = parser.Parse([]string{"--db-host", "h2"})
	require.NoError(t, err, "the flags still write into the config")
	assert.Equal(t, "h2", cli.DB.Host)
}

// A value one of Kong's resolvers supplies is not forwarded, so the loaded
// config — not the resolver — is what the field holds.
func TestLoad_KongResolverDoesNotOverrideTheLoad(t *testing.T) {
	var cli struct {
		Serve serveCmd `cmd:""`
	}
	resolver := kong.ResolverFunc(func(_ *kong.Context, _ *kong.Path, fl *kong.Flag) (any, error) {
		if fl.Name == "port" {
			return "1234", nil
		}
		return nil, nil
	})
	parser, err := kong.New(&cli, kong.Resolvers(resolver),
		Load(t.Context(), &cli.Serve.ServeConfig, []string{"APP"}, func(*kong.Context) ([]config.Option, error) {
			return []config.Option{config.WithEnviron(func() []string { return []string{"APP_PORT=9000"} })}, nil
		}))
	require.NoError(t, err)
	_, err = parser.Parse([]string{"serve"})
	require.NoError(t, err)
	assert.Equal(t, 9000, cli.Serve.Port)
}

// One prefix list names the variables in help and is the load's whole
// environment layer: an environment option among opts does not change it.
func TestLoad_OnePrefixListForHelpAndLoad(t *testing.T) {
	var cli struct {
		ServeConfig `embed:""`
	}
	environ := []string{"APP_WORKER_PORT=9001", "APP_PORT=9002", "OTHER_PORT=9003"}
	parser, err := kong.New(&cli, Load(t.Context(), &cli.ServeConfig, []string{"APP_WORKER", "APP"}, func(*kong.Context) ([]config.Option, error) {
		return []config.Option{config.WithEnvPrefix("OTHER"), config.WithEnviron(func() []string { return environ })}, nil
	}))
	require.NoError(t, err)
	for _, fl := range parser.Model.Flags {
		if fl.Name == "port" {
			assert.Equal(t, "HTTP listen port (config: port; env: APP_WORKER_PORT, APP_PORT)", fl.Help)
		}
	}
	_, err = parser.Parse(nil)
	require.NoError(t, err)
	assert.Equal(t, 9001, cli.Port)
}

func TestLoad_RejectsAConfigOutsideTheCLI(t *testing.T) {
	var cli struct {
		ServeConfig `embed:""`
	}
	var detached ServeConfig
	_, err := kong.New(&cli, Load(t.Context(), &detached, nil, nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "writes a field of")

	_, err = kong.New(&cli, Load[ServeConfig](t.Context(), nil, nil, nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil config")
}

func TestLoad_OptsErrorFailsParse(t *testing.T) {
	var cli struct {
		ServeConfig `embed:""`
	}
	boom := errors.New("boom")
	parser, err := kong.New(&cli, Load(t.Context(), &cli.ServeConfig, nil, func(*kong.Context) ([]config.Option, error) {
		return nil, boom
	}))
	require.NoError(t, err)
	_, err = parser.Parse([]string{"--port", "9000"})
	require.ErrorIs(t, err, boom)
}

type reloadCmd struct {
	ServeConfig `embed:""`

	reloader *config.Reloader[ServeConfig]
}

func (c *reloadCmd) Run(r *config.Reloader[ServeConfig]) error {
	c.reloader = r
	return nil
}

// LoadReloader binds the reloader; *cfg is a copy of its first snapshot,
// which a file edit does not change, while every reload keeps the typed
// flags. The command owns the reloader's lifecycle.
func TestLoadReloader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("port: 9000\nlevel: info\nlimits:\n  upload: 1\n"), 0o600))
	var cli struct {
		Serve reloadCmd `cmd:""`
	}
	parser, err := kong.New(&cli, LoadReloader(t.Context(), &cli.Serve.ServeConfig, nil, func(*kong.Context) ([]config.Option, error) {
		return []config.Option{config.WithRequiredFile(path)}, nil
	}))
	require.NoError(t, err)
	kctx, err := parser.Parse([]string{"serve", "--db-host", "flag-host"})
	require.NoError(t, err)
	require.NoError(t, kctx.Run())
	r := cli.Serve.reloader
	require.NotNil(t, r)
	t.Cleanup(func() { require.NoError(t, r.Stop(context.WithoutCancel(t.Context()))) })

	assert.Equal(t, *r.Config(), cli.Serve.ServeConfig)
	assert.Equal(t, uint64(1), r.Snapshot().Generation)
	cli.Serve.Limits["upload"] = 99
	assert.Equal(t, 1, r.Config().Limits["upload"], "*cfg shares no storage with the published config")

	require.NoError(t, os.WriteFile(path, []byte("port: 9100\nlevel: warn\ndb:\n  host: file-host\n"), 0o600))
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 9100, r.Config().Port)
	assert.Equal(t, "warn", r.Config().Level)
	assert.Equal(t, "flag-host", r.Config().DB.Host, "the startup command line still wins")
	assert.Equal(t, uint64(2), res.Snapshot.Generation)
	assert.Equal(t, 9000, cli.Serve.Port, "*cfg is the startup config")

	require.NoError(t, r.Arm(t.Context()), "arming is the command's")
}

// No *T is bound for a reloading command: a Run method that asks for one
// fails instead of reading a startup copy as if it were live.
func TestLoadReloader_BindsNoStaticConfig(t *testing.T) {
	var cli struct {
		Serve serveCmd `cmd:""`
	}
	parser, err := kong.New(&cli, LoadReloader(t.Context(), &cli.Serve.ServeConfig, []string{"APP"}, func(*kong.Context) ([]config.Option, error) {
		return []config.Option{config.WithEnviron(func() []string { return []string{"APP_PORT=9000"} })}, nil
	}))
	require.NoError(t, err)
	kctx, err := parser.Parse([]string{"serve"})
	require.NoError(t, err)
	assert.Equal(t, 9000, cli.Serve.Port)
	require.Error(t, kctx.Run(), "Run(*ServeConfig) has nothing bound")
}

func TestLoadReloader_FailedInitialLoad(t *testing.T) {
	var cli struct {
		Serve reloadCmd `cmd:""`
	}
	parser, err := kong.New(&cli, LoadReloader(t.Context(), &cli.Serve.ServeConfig, nil, nil))
	require.NoError(t, err)
	_, err = parser.Parse([]string{"serve"})
	require.ErrorIs(t, err, config.ErrValidation, "the defaults fail Validate in the reloader's initial load")
}
