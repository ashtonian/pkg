package config

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type credentialExample struct{ name, input, secret, keep string }

func credentialExamples() []credentialExample {
	return []credentialExample{
		{"URL", "postgres://alice:example-password@db.test/app?sslmode=require", "example-password", "alice:"},
		{"URL context", "connect postgres://alice:example-password@db.test/app next", "example-password", "db.test/app next"},
		{"URL token", "https://example-token@api.test/path", "example-token", "api.test/path"},
		{"malformed URL", "postgres://alice:example/password@db.test/app", "example/password", "db.test/app"},
		{"DSN quoted", "host=db.test user=alice password='example password' dbname=app", "example password", "dbname=app"},
		{"DSN escaped", `user=alice password='example\'password' dbname=app`, "password'", "dbname=app"},
		{"DSN doubled quote", "user=alice password='example''password' dbname=app", "example", "dbname=app"},
		{"query", "https://api.test/path?access_token=example-token&limit=10", "example-token", "&limit=10"},
		{"JSON", `{"password":"example-password","name":"alice"}`, "example-password", `"name":"alice"`},
		{"env", "APP_API_KEY=example-key", "example-key", "APP_API_KEY="},
		{"short bearer", "Authorization: Bearer abc", "abc", "Authorization: Bearer "},
		{"basic", "Proxy-Authorization: Basic YWxpY2U6cGFzc3dvcmQ=", "YWxpY2U6cGFzc3dvcmQ=", "Basic "},
		{"PEM", "start\n-----BEGIN PRIVATE KEY-----\nexample-key\n-----END PRIVATE KEY-----\nend", "example-key", "\nend"},
		{"truncated PEM", "-----BEGIN PRIVATE KEY-----\nexample-key", "example-key", redactedValue},
		{"JWT", "value=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhbGljZSJ9.c2lnbmF0dXJl", "eyJzdWIiOiJhbGljZSJ9", "value="},
	}
}

func TestRedactText(t *testing.T) {
	for _, tc := range credentialExamples() {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactText(tc.input)
			assert.NotContains(t, got, tc.secret)
			assert.Contains(t, got, tc.keep)
			assert.Equal(t, got, RedactText(got), "redaction is idempotent")
		})
	}
	for _, ordinary := range []string{"https://api.test/path@public", "release.v1.0", "timeout=30s", "name=alice", redactedValue, "http://localhost:8080"} {
		assert.Equal(t, ordinary, RedactText(ordinary))
	}
	assert.JSONEq(t, `{"password":"[REDACTED]","name":"alice"}`, RedactText(`{"password":"secret","name":"alice"}`))
}

type shapeConfig struct {
	Endpoint string            `koanf:"endpoint" sensitive:"false"`
	Values   []string          `koanf:"values"`
	Named    map[string]string `koanf:"named"`
}

func TestCredentialShapesAcrossDiagnostics(t *testing.T) {
	for _, tc := range credentialExamples() {
		t.Run(tc.name, func(t *testing.T) {
			value := tc.input
			cfg, err := LoadSnapshot[shapeConfig](t.Context(), WithDefaults(map[string]any{
				"endpoint": value, "values": []string{value}, "named": map[string]string{"service": value},
			}))
			require.NoError(t, err)
			var buf bytes.Buffer
			for _, format := range []string{FormatTable, FormatJSON, FormatYAML} {
				buf.Reset()
				require.NoError(t, PrintRedacted(cfg.Config, WithWriter(&buf), WithFormat(format)))
				assert.NotContains(t, buf.String(), tc.secret)
				if format == FormatTable {
					assert.Contains(t, buf.String(), tc.keep)
				}
			}
			buf.Reset()
			require.NoError(t, PrintExplain(cfg, WithWriter(&buf), WithFormat(FormatJSON)))
			assert.NotContains(t, buf.String(), tc.secret)
			tree, err := RedactedTreeChecked(cfg.Config)
			require.NoError(t, err)
			b, err := json.Marshal(tree)
			require.NoError(t, err)
			assert.NotContains(t, string(b), tc.secret)
			changes, err := Diff(&shapeConfig{}, cfg.Config)
			require.NoError(t, err)
			for _, c := range changes {
				assert.NotContains(t, c.New.Text, tc.secret)
				if strings.Contains(c.New.Text, "api.test") {
					assert.True(t, c.New.Redacted)
				}
			}
			raw, err := Tree(cfg.Config)
			require.NoError(t, err)
			assert.Equal(t, value, raw["endpoint"])
			assert.Equal(t, value, cfg.Config.Endpoint, "rendering cannot mutate the source")
		})
	}
}

func TestCredentialDefaultsAreWithheldInsteadOfChangingTemplates(t *testing.T) {
	type config struct {
		Endpoint string `koanf:"endpoint" default:"https://alice:example-password@api.test/path"`
	}
	rows, err := Usage[config]("")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.True(t, rows[0].Withheld)
	assert.Empty(t, rows[0].Default)
	schema := jsonSchemaOf[config](t)
	assert.NotContains(t, prop(t, schema, "endpoint"), "default")
	var buf bytes.Buffer
	require.NoError(t, PrintTemplate[config]("", WithFormat(FormatJSON), WithWriter(&buf)))
	assert.NotContains(t, buf.String(), "example-password")
	var input map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &input))
	got, err := Decode[config](t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, "https://alice:example-password@api.test/path", got.Endpoint)
}

func TestCredentialByteRepresentations(t *testing.T) {
	type config struct {
		Data  []byte
		Fixed [10]byte
	}
	var cfg config
	cfg.Data = []byte("Bearer abc")
	copy(cfg.Fixed[:], cfg.Data)
	tree, err := RedactedTreeChecked(&cfg)
	require.NoError(t, err)
	assert.Equal(t, "Bearer [REDACTED]", tree["data"])
	assert.Equal(t, "Bearer [REDACTED]", tree["fixed"])
	var buf bytes.Buffer
	require.NoError(t, PrintRedacted(&cfg, WithWriter(&buf)))
	assert.Equal(t, 2, strings.Count(buf.String(), "Bearer [REDACTED]"))
	diff, err := Diff(&config{}, &cfg)
	require.NoError(t, err)
	for _, change := range diff {
		assert.True(t, change.New.Redacted)
		assert.Equal(t, "Bearer [REDACTED]", change.New.Text)
	}
	raw, err := Tree(&cfg)
	require.NoError(t, err)
	assert.Equal(t, "Bearer abc", raw["data"])
	assert.Len(t, raw["fixed"], 10)
}
