package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/config"
)

// parse parses args with the service's own Kong options.
func parse(t *testing.T, args ...string) (*CLI, *kong.Context, *slog.LevelVar, error) {
	t.Helper()
	var cli CLI
	level := new(slog.LevelVar)
	opts, err := loading(t.Context(), &cli, slog.New(slog.DiscardHandler), level)
	require.NoError(t, err)
	parser, err := kong.New(&cli, opts...)
	require.NoError(t, err)
	kctx, err := parser.Parse(args)
	return &cli, kctx, level, err
}

func TestService(t *testing.T) {
	file := filepath.Join(t.TempDir(), "service.yaml")
	write := func(content string) { require.NoError(t, os.WriteFile(file, []byte(content), 0o600)) }
	write("greeting: hi\nadmin_token: t0ken\nlog:\n  level: warn\n")

	cli, kctx, level, err := parse(t, "--config", file, "--log-level", "debug")
	require.NoError(t, err)
	bound, err := kctx.Call(func(r *config.Reloader[AppConfig]) *config.Reloader[AppConfig] { return r })
	require.NoError(t, err)
	r := bound[0].(*config.Reloader[AppConfig])
	t.Cleanup(func() { _ = r.Stop(t.Context()) })
	assert.Equal(t, slog.LevelDebug, level.Level(), "the flag beats the file, applied before serving")
	assert.Equal(t, "t0ken", cli.Serve.AdminToken, "the command holds the loaded config, fields with no flag included")

	srv := httptest.NewServer(routes(r))
	t.Cleanup(srv.Close)
	get := func(path, token string) (int, string) {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := srv.Client().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, string(b)
	}
	code, body := get("/", "")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "hi\n", body)
	code, _ = get("/admin/config", "wrong")
	assert.Equal(t, http.StatusUnauthorized, code)

	write("greeting: hello again\nadmin_token: t0ken\nlog:\n  level: error\nhttp:\n  addr: :9090\n")
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	_, body = get("/", "")
	assert.Equal(t, "hello again\n", body, "read from Config on every request")
	assert.Equal(t, slog.LevelDebug, level.Level(), "the flag still wins")
	var pending []string
	for _, c := range res.Snapshot.PendingRestart {
		pending = append(pending, c.Key)
	}
	assert.Equal(t, []string{"http.addr"}, pending, "the listener's address waits for a restart")
	assert.Equal(t, ":8080", cli.Serve.HTTP.Addr, "the listener keeps the startup config")

	code, body = get("/admin/config", "t0ken")
	assert.Equal(t, http.StatusOK, code)
	assert.Contains(t, body, `"pending_restart"`)
	assert.NotContains(t, body, "t0ken")

	write("greeting: \"  \"\nadmin_token: t0ken\n")
	_, err = r.Reload(t.Context())
	require.ErrorIs(t, err, config.ErrValidation, "Validate refuses a blank greeting")
	_, body = get("/", "")
	assert.Equal(t, "hello again\n", body)
}

// The environment alone configures a command, and Validate judges the
// loaded config — the environment's blank greeting — not Kong's defaults.
func TestServiceEnvironmentOnly(t *testing.T) {
	t.Setenv("APP_ADMIN_TOKEN", "t0ken")
	t.Setenv("APP_GREETING", "from the environment")
	cli, _, _, err := parse(t, "--config", filepath.Join(t.TempDir(), "absent.yaml"), "print")
	require.NoError(t, err)
	assert.Equal(t, "from the environment", cli.Print.Greeting)

	t.Setenv("APP_GREETING", " ")
	_, _, _, err = parse(t, "--config", filepath.Join(t.TempDir(), "absent.yaml"), "print")
	require.ErrorIs(t, err, config.ErrValidation, "the loaded greeting is blank, though Kong's default is not")
}

// The commands that hold no config run without one: no admin token here.
func TestServiceCommandsWithoutConfig(t *testing.T) {
	for _, command := range []string{"template", "schema"} {
		_, _, _, err := parse(t, "--config", filepath.Join(t.TempDir(), "absent.yaml"), command)
		require.NoError(t, err, command)
	}
}

// The config file's JSON Schema is a valid Draft 2020-12 schema, as an
// independent validator reads it, that accepts a good file and refuses an
// unknown key and a value outside a oneof.
func TestServiceSchema(t *testing.T) {
	raw, err := config.JSONSchema[AppConfig]()
	require.NoError(t, err)
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	require.NoError(t, err)
	c := jsonschema.NewCompiler()
	require.NoError(t, c.AddResource("service.schema.json", doc))
	schema, err := c.Compile("service.schema.json")
	require.NoError(t, err, "the schema must be valid against the Draft 2020-12 metaschema")

	check := func(file string) error {
		v, err := jsonschema.UnmarshalJSON(strings.NewReader(file))
		require.NoError(t, err)
		return schema.Validate(v)
	}
	assert.NoError(t, check(`{"greeting": "hi", "admin_token": "t", "log": {"level": "warn"}, "http": {"addr": ":9090", "read_header_timeout": "2s"}}`))
	assert.Error(t, check(`{"log": {"level": "loud"}}`), "outside the oneof")
	assert.Error(t, check(`{"greting": "hi"}`), "a key no field takes")
	assert.Error(t, check(`{"http": {"read_header_timeout": 5}}`), "a duration is written as text")
}
