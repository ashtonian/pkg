package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnknownInput_AdjacentTranspositions(t *testing.T) {
	type mail struct {
		From string `koanf:"from"`
		Port int    `koanf:"port"`
		Host string `koanf:"host"`
	}
	type cfg struct {
		Mail mail `koanf:"mail"`
	}
	_, err := Decode[cfg](t.Context(), map[string]any{"mail": map[string]any{"prot": 25, "hots": "localhost"}})
	var unknown *UnknownKeysError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, []UnknownKey{{Key: "mail.hots", Suggestion: "mail.host"}, {Key: "mail.prot", Suggestion: "mail.port"}}, unknown.Keys)
	_, err = Load[cfg](t.Context(), WithEnvPrefix("APP"), WithEnviron(func() []string { return []string{"APP_MAIL_HOTS=localhost", "APP_MAIL_PROT=25"} }), WithUnknownKeys(func(_ context.Context, report *UnknownKeysError) error {
		assert.Equal(t, []UnknownKey{{Key: "APP_MAIL_HOTS", Suggestion: "APP_MAIL_HOST"}, {Key: "APP_MAIL_PROT", Suggestion: "APP_MAIL_PORT"}}, report.Variables)
		return nil
	}))
	require.NoError(t, err)
}

func TestSensitivity_NumericCredentialNames(t *testing.T) {
	type cfg struct {
		AllowCredentials bool          `koanf:"allow_credentials"`
		TokenCount       int           `koanf:"token_count"`
		KeyRotation      time.Duration `koanf:"key_rotation"`
		Password         string        `koanf:"password"`
		SecretCount      int           `koanf:"secret_count" sensitive:"true"`
	}
	for _, key := range []string{"allow_credentials", "token_count", "key_rotation"} {
		f, err := FieldFor[cfg](key)
		require.NoError(t, err)
		assert.False(t, f.Sensitive, key)
	}
	changes, err := Diff(&cfg{}, &cfg{AllowCredentials: true, TokenCount: 3, KeyRotation: time.Minute, Password: "secret", SecretCount: 3})
	require.NoError(t, err)
	for _, change := range changes {
		assert.Equal(t, change.Key == "password" || change.Key == "secret_count", change.Field.Sensitive, change.Key)
	}
	tree := RedactedTree(cfg{AllowCredentials: true, TokenCount: 3, KeyRotation: time.Minute, Password: "secret", SecretCount: 3})
	assert.Equal(t, true, tree["allow_credentials"])
	assert.Equal(t, int64(3), tree["token_count"])
	assert.Equal(t, "1m0s", tree["key_rotation"])
	assert.Equal(t, redactedValue, tree["password"])
	assert.Equal(t, redactedValue, tree["secret_count"])
	_, err = Decode[cfg](t.Context(), map[string]any{"allow_credentials": "maybe"})
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "'allow_credentials' cannot parse value as 'bool'")
	assert.NotContains(t, err.Error(), "sensitive")
}

func TestSnapshot_UnknownInputFollowsPublication(t *testing.T) {
	type cfg struct {
		Port int `koanf:"port"`
	}
	data := map[string]any{"port": 1, "first": true}
	p, err := NewMapProvider(func(context.Context) (map[string]any, error) { return data, nil }, "")
	require.NoError(t, err)
	abandon, reject := false, false
	var cancelAttempt context.CancelFunc
	opts := []Option{WithBaseProvider(p, nil), WithUnknownKeys(func(_ context.Context, u *UnknownKeysError) error {
		if reject {
			return u
		}
		return nil
	}), WithPostLoad(func(ctx context.Context, _ *cfg) error {
		if abandon {
			cancelAttempt()
			return ctx.Err()
		}
		return nil
	})}
	r, err := NewReloader[cfg](t.Context(), opts)
	require.NoError(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, r.Stop(ctx))
	})
	keys := func(s Snapshot[cfg]) []UnknownKey {
		b, err := json.Marshal(s)
		require.NoError(t, err)
		var v struct {
			UnknownKeys *struct {
				Keys []UnknownKey `json:"keys"`
			} `json:"unknown_keys"`
		}
		require.NoError(t, json.Unmarshal(b, &v))
		if v.UnknownKeys == nil {
			return nil
		}
		return v.UnknownKeys.Keys
	}
	assert.Equal(t, []UnknownKey{{Key: "first"}}, keys(r.Snapshot()))
	s, err := LoadSnapshot[cfg](t.Context(), opts...)
	require.NoError(t, err)
	assert.Equal(t, []UnknownKey{{Key: "first"}}, keys(s))
	data = map[string]any{"port": 2, "rejected": true}
	reject = true
	_, err = r.Reload(t.Context())
	require.Error(t, err)
	assert.Equal(t, []UnknownKey{{Key: "first"}}, keys(r.Snapshot()))
	reject, abandon = false, true
	attempt, cancel := context.WithCancel(t.Context())
	defer cancel()
	cancelAttempt = cancel
	_, err = r.Reload(attempt)
	require.True(t, errors.Is(err, context.Canceled))
	assert.Equal(t, []UnknownKey{{Key: "first"}}, keys(r.Snapshot()))
	abandon = false
	data = map[string]any{"port": 3}
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Nil(t, keys(res.Snapshot))
	assert.Nil(t, keys(r.Snapshot()))
}

func TestUsage_MarkdownEscapesCells(t *testing.T) {
	type cfg struct {
		Text string "koanf:\"text\" default:\"a|b\" desc:\"a `quote` and pipe| and newline\\nnext\""
	}
	var out bytes.Buffer
	err := PrintUsage[cfg]("APP", WithFormat("markdown"), WithWriter(&out))
	require.NoError(t, err)
	assert.Contains(t, out.String(), "a\\|b")
	assert.Contains(t, out.String(), "a \\`quote\\` and pipe\\| and newline<br>next")
}

func TestUnknownInput_ReferencedVariables(t *testing.T) {
	type ref struct {
		Name string `koanf:"name" envref:"true"`
	}
	type cfg struct {
		PasswordEnv string   `koanf:"password_env" envref:"true"`
		Names       []string `koanf:"names" envref:"true"`
		Items       []ref    `koanf:"items"`
	}
	var report *UnknownKeysError
	got, err := Decode[cfg](t.Context(), map[string]any{"password_env": "APP_PASSWORD", "names": []string{"APP_FIRST", "APP_SECOND"}, "items": []any{map[string]any{"name": "APP_ITEM"}}},
		WithEnvPrefix("APP"), WithEnviron(func() []string {
			return []string{"APP_PASSWORD=s", "APP_FIRST=a", "APP_SECOND=b", "APP_ITEM=c", "APP_TYPO=x"}
		}),
		WithUnknownKeys(func(_ context.Context, u *UnknownKeysError) error { report = u; return nil }))
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "APP_PASSWORD", got.PasswordEnv)
	require.NotNil(t, report)
	assert.Equal(t, []UnknownKey{{Key: "APP_TYPO"}}, report.Variables)
}

func TestUnknownInput_ClaimsAndEffectiveReferences(t *testing.T) {
	type ref struct {
		Name string `koanf:"name" envref:"true"`
	}
	type cfg struct {
		Name string         `koanf:"name" envref:"true" default:"APP_DEFAULT" env:"ref"`
		Refs map[string]ref `koanf:"refs"`
	}
	var report *UnknownKeysError
	opts := []Option{WithEnvPrefix("APP"), WithKnownVariables("APP_KNOWN"), WithVariableFilter(func(name string) bool { return strings.HasPrefix(name, "APP_EXTERNAL_") }),
		WithEnviron(func() []string {
			return []string{"ref=APP_REPLACED", "APP_DEFAULT=secret", "APP_REPLACED=secret", "APP_MAP=secret", "APP_KNOWN=secret", "APP_EXTERNAL_ONE=secret", "APP_WRONG=secret"}
		}),
		WithUnknownKeys(func(_ context.Context, u *UnknownKeysError) error { report = u; return nil })}
	got, err := Decode[cfg](t.Context(), map[string]any{"refs": map[string]any{"a": map[string]any{"name": "APP_MAP"}}, "wrong_key": "secret"}, opts...)
	require.NoError(t, err)
	assert.Equal(t, "APP_REPLACED", got.Name, "env:ref retains its alternate-name meaning")
	require.NotNil(t, report)
	assert.Equal(t, []UnknownKey{{Key: "wrong_key"}}, report.Keys)
	var names []string
	for _, v := range report.Variables {
		names = append(names, v.Key)
	}
	assert.Equal(t, []string{"APP_DEFAULT", "APP_WRONG"}, names)
	assert.NotContains(t, report.Error(), "secret")
}

func TestUnknownInput_InvalidReferencesAreSchemaErrors(t *testing.T) {
	_, err := FieldsChecked[struct {
		Value int `koanf:"value" envref:"true"`
	}]()
	require.ErrorIs(t, err, ErrSchema)
	_, err = FieldsChecked[struct {
		Value string `koanf:"value" envref:"yes"`
	}]()
	require.ErrorIs(t, err, ErrSchema)
	_, err = FieldsChecked[struct {
		Ignored int `koanf:"-" envref:"true"`
	}]()
	require.NoError(t, err)
}

func TestUnknownInput_ClaimsFilterDebugDiagnostics(t *testing.T) {
	type cfg struct {
		Reference string `koanf:"reference" envref:"true" default:"APP_SECRET"`
	}
	for _, handler := range []bool{false, true} {
		var out bytes.Buffer
		opts := []Option{
			WithEnvPrefix("APP"),
			WithEnviron(func() []string {
				return []string{"APP_SECRET=hidden", "APP_KNOWN=hidden", "APP_EXTERNAL=hidden", "APP_TYPO=hidden"}
			}),
			WithKnownVariables("APP_KNOWN"),
			WithVariableFilter(func(name string) bool { return name == "APP_EXTERNAL" }),
			WithLogger(slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))),
		}
		if handler {
			opts = append(opts, WithAllowUnknownKeys())
		}
		_, err := Load[cfg](t.Context(), opts...)
		require.NoError(t, err)
		assert.Contains(t, out.String(), "APP_TYPO")
		for _, name := range []string{"APP_SECRET", "APP_KNOWN", "APP_EXTERNAL", "hidden"} {
			assert.NotContains(t, out.String(), name, "handler=%v", handler)
		}
	}
}

func TestSnapshot_UnknownInputCopiesAndApplyFailure(t *testing.T) {
	type cfg struct {
		Port int `koanf:"port"`
	}
	value := 1
	var handled *UnknownKeysError
	p, err := NewMapProvider(func(context.Context) (map[string]any, error) {
		return map[string]any{"port": value, "extra": true}, nil
	}, "")
	require.NoError(t, err)
	r, err := NewReloader[cfg](t.Context(), []Option{WithBaseProvider(p, nil), WithUnknownKeys(func(_ context.Context, u *UnknownKeysError) error { handled = u; return nil })},
		WithApplier(Applier[cfg]{Name: "port", Prefixes: []string{"port"}, Apply: func(context.Context, ApplyEvent[cfg]) (ApplyResult, error) {
			return ApplyResult{}, errors.New("apply failed")
		}}),
		WithOnReloadSuccess(func(_ context.Context, ev ReloadEvent[cfg]) { ev.Result.Snapshot.UnknownKeys.Keys[0].Key = "callback" }))
	require.NoError(t, err)
	stopReloader(t, r)
	handled.Keys[0].Key = "handler"
	snapshot := r.Snapshot()
	require.NotNil(t, snapshot.UnknownKeys)
	assert.Equal(t, "extra", snapshot.UnknownKeys.Keys[0].Key)
	snapshot.UnknownKeys.Keys[0].Key = "reader"
	assert.Equal(t, "extra", r.Snapshot().UnknownKeys.Keys[0].Key)
	value++
	result, err := r.Reload(t.Context())
	require.ErrorIs(t, err, ErrApply)
	assert.Equal(t, uint64(2), result.Snapshot.Generation)
	assert.Equal(t, "extra", result.Snapshot.UnknownKeys.Keys[0].Key)
	result.Snapshot.UnknownKeys.Keys[0].Key = "result"
	assert.Equal(t, "extra", r.Snapshot().UnknownKeys.Keys[0].Key)
}

type numericCredential int

func (v *numericCredential) UnmarshalText([]byte) error { *v = 1; return nil }

func TestSensitivity_ContainersAndExplicitTags(t *testing.T) {
	type child struct {
		Count int `koanf:"count" sensitive:"false"`
	}
	type cfg struct {
		TokenCounts []int             `koanf:"token_counts"`
		Values      map[string]bool   `koanf:"values"`
		AuthCode    numericCredential `koanf:"auth_code"`
		Inherited   child             `koanf:"inherited" sensitive:"true"`
		Password    string            `koanf:"password" sensitive:"false"`
	}
	for key, want := range map[string]bool{"token_counts": false, `values["password"]`: false, "auth_code": true, "inherited.count": true, "password": false} {
		f, err := FieldFor[cfg](key)
		require.NoError(t, err)
		assert.Equal(t, want, f.Sensitive, key)
	}
	old, next := cfg{Values: map[string]bool{"password": false}}, cfg{Values: map[string]bool{"password": true}}
	changes, err := Diff(&old, &next)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	assert.False(t, changes[0].New.Redacted)
	assert.Equal(t, "true", changes[0].New.Text)
	assert.Equal(t, map[string]any{"password": true}, RedactedTree(next)["values"])
}
