package config

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/knadh/koanf/providers/confmap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type declaredConfig struct {
	Retry struct {
		Attempts int           `koanf:"attempts" default:"1"`
		Delay    time.Duration `koanf:"delay" default:"1s"`
	} `koanf:"retry"`
	Zero     int      `koanf:"zero" default:"7"`
	Label    string   `koanf:"label"`
	Names    []string `koanf:"names"`
	Password string   `koanf:"password"`
}

func (*declaredConfig) ConfigDefaults() map[string]any {
	return map[string]any{"retry.attempts": 3, "retry.delay": "2s", "zero": 0,
		"label": "", "names": []string{"one", "two"}, "password": "declared-secret"}
}

func TestSchemaDefaultsAcrossLoadingAndMetadata(t *testing.T) {
	snap, err := LoadSnapshot[declaredConfig](t.Context())
	require.NoError(t, err)
	assert.Equal(t, 3, snap.Config.Retry.Attempts)
	assert.Equal(t, 2*time.Second, snap.Config.Retry.Delay)
	assert.Zero(t, snap.Config.Zero)
	assert.Equal(t, SourceSchemaDefaults, snap.Provenance["retry.attempts"].Input.Kind)
	rows, err := Usage[declaredConfig]("APP")
	require.NoError(t, err)
	byKey := map[string]UsageEntry{}
	for _, row := range rows {
		byKey[row.Key] = row
	}
	assert.Equal(t, "3", byKey["retry.attempts"].Default)
	assert.Equal(t, "2s", byKey["retry.delay"].Default)
	assert.Equal(t, "0", byKey["zero"].Default)
	assert.True(t, byKey["label"].hasDefault)
	assert.True(t, byKey["password"].Withheld)
	schema := jsonSchemaOf[declaredConfig](t)
	assert.EqualValues(t, 3, prop(t, prop(t, schema, "retry"), "attempts")["default"])
	assert.Equal(t, "2s", prop(t, prop(t, schema, "retry"), "delay")["default"])
	assert.NotContains(t, prop(t, schema, "password"), "default")
	var buf bytes.Buffer
	require.NoError(t, PrintTemplate[declaredConfig]("", WithFormat(FormatJSON), WithWriter(&buf)))
	assert.NotContains(t, buf.String(), "declared-secret")
	var tree map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &tree))
	assert.Contains(t, tree, "label")
	got, err := Decode[declaredConfig](t.Context(), tree)
	require.NoError(t, err)
	assert.Equal(t, snap.Config, got)
	got, err = Load[declaredConfig](t.Context(), WithDefaults(map[string]any{"retry.attempts": 4}))
	require.NoError(t, err)
	assert.Equal(t, 4, got.Retry.Attempts)
	got, err = Load[declaredConfig](t.Context(), WithDefaults(map[string]any{"retry.attempts": 4}),
		WithBaseProvider(confmap.Provider(map[string]any{"retry.attempts": 5}, "."), nil))
	require.NoError(t, err)
	assert.Equal(t, 5, got.Retry.Attempts)
}

type unknownDeclaredConfig struct {
	Port int `koanf:"port"`
}

func (*unknownDeclaredConfig) ConfigDefaults() map[string]any { return map[string]any{"prot": 2} }

type invalidDeclaredConfig struct {
	Port int `koanf:"port"`
}

func (*invalidDeclaredConfig) ConfigDefaults() map[string]any { return map[string]any{"port": "bad"} }

func TestSchemaDefaultsRejectInvalidDeclarations(t *testing.T) {
	_, err := Load[unknownDeclaredConfig](t.Context(), WithUnknownKeys(func(_ context.Context, _ *UnknownKeysError) error { return nil }))
	require.ErrorIs(t, err, ErrSchema)
	_, err = Usage[unknownDeclaredConfig]("")
	require.ErrorIs(t, err, ErrSchema)
	_, err = JSONSchema[unknownDeclaredConfig]()
	require.ErrorIs(t, err, ErrSchema)
	err = PrintTemplate[unknownDeclaredConfig]("", WithWriter(&bytes.Buffer{}))
	require.ErrorIs(t, err, ErrSchema)
	_, err = Load[invalidDeclaredConfig](t.Context(), WithDefaults(map[string]any{"port": 8}))
	require.ErrorIs(t, err, ErrSchema)
	assert.Equal(t, KindSchema, Diagnose(err).Kind)
}

type declaredBlock struct {
	Count int    `koanf:"count" default:"2"`
	Label string `koanf:"label"`
}

type declaredBranches struct {
	Off     *declaredBlock  `koanf:"off"`
	On      declaredBlock   `koanf:"on"`
	Entries []declaredBlock `koanf:"entries"`
}

func (*declaredBranches) ConfigDefaults() map[string]any {
	return map[string]any{"off": nil, "on": declaredBlock{Count: 9, Label: "declared"}, "entries": []any{map[string]any{}}}
}

func TestSchemaDefaultsPreserveNullAndStructPresence(t *testing.T) {
	want, err := Load[declaredBranches](t.Context())
	require.NoError(t, err)
	require.Nil(t, want.Off)
	require.Equal(t, 9, want.On.Count)
	require.Equal(t, 2, want.Entries[0].Count)
	rows, err := Usage[declaredBranches]("")
	require.NoError(t, err)
	for _, r := range rows {
		if r.Key == "off.count" {
			assert.False(t, r.hasDefault)
		}
		if r.Key == "on.label" {
			assert.Equal(t, "declared", r.Default)
			assert.True(t, r.hasDefault)
		}
	}
	found := false
	for _, r := range rows {
		if r.Key == "entries" {
			found = true
			assert.JSONEq(t, `[{"count":2,"label":""}]`, r.Default)
		}
	}
	assert.True(t, found, "collection default needs a catalog row")
	var buf bytes.Buffer
	require.NoError(t, PrintTemplate[declaredBranches]("", WithWriter(&buf), WithFormat(FormatJSON)))
	var tree map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &tree))
	got, err := Decode[declaredBranches](t.Context(), tree)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	schema := jsonSchemaOf[declaredBranches](t)
	assert.Contains(t, prop(t, schema, "off"), "default")
	assert.Equal(t, "declared", prop(t, prop(t, schema, "on"), "label")["default"])
}

type overlappingDefaults struct {
	Nested declaredBlock `koanf:"nested"`
	Other  string        `koanf:"nested-other"`
}

func (*overlappingDefaults) ConfigDefaults() map[string]any {
	return map[string]any{"nested": map[string]any{"count": 2}, "nested-other": "kept", "nested.count": 3}
}

type sensitiveUnknownDefault struct {
	Connections map[string]declaredBlock `koanf:"connections" sensitive:"true"`
}

func (*sensitiveUnknownDefault) ConfigDefaults() map[string]any {
	return map[string]any{"connections": map[string]any{"credential-example": map[string]any{"unknown": true}}}
}

func TestSchemaDefaultsRejectAmbiguityAndProtectUnknownPaths(t *testing.T) {
	_, err := Load[overlappingDefaults](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "both set")
	_, err = Load[sensitiveUnknownDefault](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.NotContains(t, err.Error(), "credential-example")
	var unknown *UnknownKeysError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, []UnknownKey{{Key: "connections"}}, unknown.Keys)
}
