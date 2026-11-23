package config

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shared is unexported, as a package's common settings often are.
type shared struct {
	Order    int    `koanf:"order" default:"5" desc:"Sort order"`
	Name     string `koanf:"name"`
	Password string `koanf:"password" secret:"db#password"`
}

type withShared struct {
	shared `koanf:",squash"`
	Port   int `koanf:"port"`
}

// An unexported embedded struct that is squashed contributes its fields to
// every part of the package, as decoding already filled them: tag defaults,
// variables, strict keys, secrets and every output.
func TestUnexportedSquash_IsPromoted(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "name: x\nport: 1\n")
	fetch := func(context.Context, string) (string, error) { return `{"password":"s3cret"}`, nil }
	opts := []Option{WithFile(file), WithEnvPrefix("APP"), WithSkipValidation(),
		WithEnviron(func() []string { return []string{"APP_ORDER=7"} }),
		WithSecretPopulator(SecretsFrom(fetch))}

	got, err := Load[withShared](t.Context(), opts...)
	require.NoError(t, err)
	assert.Equal(t, withShared{shared: shared{Order: 7, Name: "x", Password: "s3cret"}, Port: 1}, *got)

	got, err = Load[withShared](t.Context(), WithFile(file), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, 5, got.Order, "the embedded struct's default tag applies")

	typo := filepath.Join(t.TempDir(), "typo.yaml")
	writeFile(t, typo, "nmae: x\n")
	_, err = Load[withShared](t.Context(), WithFile(typo), WithSkipValidation())
	var unknown *UnknownKeysError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, []UnknownKey{{Key: "nmae", Suggestion: "name"}}, unknown.Keys)

	paths := []string{}
	for _, f := range Fields[withShared]() {
		paths = append(paths, f.Path)
	}
	assert.Equal(t, []string{"order", "name", "password", "port"}, paths)

	key, err := KeyOf(got, &got.Order)
	require.NoError(t, err)
	assert.Equal(t, "order", key)

	var usage bytes.Buffer
	require.NoError(t, PrintUsage[withShared]("APP", WithWriter(&usage)))
	assert.Contains(t, usage.String(), "APP_ORDER")
	assert.Contains(t, usage.String(), "Sort order")

	raw, err := JSONSchema[withShared]()
	require.NoError(t, err)
	var doc struct {
		Properties map[string]any `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	assert.ElementsMatch(t, []string{"order", "name", "password", "port"}, slices.Collect(maps.Keys(doc.Properties)))

	var tmpl bytes.Buffer
	require.NoError(t, PrintTemplate[withShared]("APP", WithWriter(&tmpl)))
	assert.Contains(t, tmpl.String(), "order: 5\n")

	loaded, err := Load[withShared](t.Context(), opts...)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"order": "7", "name": "x", "password": redactedValue, "port": "1"}, RedactedMap(loaded))
	var env bytes.Buffer
	require.NoError(t, PrintEnv(loaded, "APP", WithWriter(&env)))
	assert.Contains(t, env.String(), "export APP_ORDER='7'")
	assert.NotContains(t, env.String(), "s3cret")

	next := *loaded
	next.Order = 9
	changes, err := Diff(loaded, &next)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	assert.Equal(t, "order", changes[0].Key)

	snap, err := LoadSnapshot[withShared](t.Context(), opts...)
	require.NoError(t, err)
	assert.Equal(t, "APP_ORDER", snap.Provenance["order"].Input.Name)
	var explain bytes.Buffer
	require.NoError(t, PrintExplain(snap, WithWriter(&explain)))
	assert.Contains(t, explain.String(), "environment APP_ORDER")
}

// A reloader publishes and applies changes to the promoted keys.
func TestUnexportedSquash_Reloads(t *testing.T) {
	var mu sync.Mutex
	settings := map[string]any{"order": 1}
	src, err := NewMapProvider(func(context.Context) (map[string]any, error) {
		mu.Lock()
		defer mu.Unlock()
		return maps.Clone(settings), nil
	}, "")
	require.NoError(t, err)
	var applied []int
	r, err := NewReloader[withShared](t.Context(), []Option{WithBaseProvider(src, nil), WithSkipValidation()},
		WithApplier(Applier[withShared]{Name: "order", Prefixes: []string{"order"},
			Apply: func(_ context.Context, ev ApplyEvent[withShared]) (ApplyResult, error) {
				applied = append(applied, ev.Desired.Order)
				return ApplyResult{}, nil
			}}))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, r.Stop(context.Background())) })

	mu.Lock()
	settings = map[string]any{"order": 2}
	mu.Unlock()
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 2, r.Config().Order)
	require.Len(t, res.Changes, 1)
	assert.Equal(t, "order", res.Changes[0].Key)
	assert.Equal(t, []int{2}, applied)
}

// An embedded pointer to an unexported struct cannot be allocated, so it is
// refused rather than silently left unset.
func TestUnexportedSquash_PointerRefused(t *testing.T) {
	type viaPointer struct {
		*shared `koanf:",squash"`
		Port    int `koanf:"port"`
	}
	_, err := Load[viaPointer](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "squashes a pointer to an unexported struct")
}

// promotedLists is unexported; its lists and map reach the config only by
// promotion through a squashed embedding.
type promotedLists struct {
	Hosts  []string          `koanf:"hosts" default:"a,b"`
	Labels map[string]string `koanf:"labels" default:"team=core"`
}

type promotedListsCfg struct {
	promotedLists `koanf:",squash"`
	Port          int `koanf:"port"`
}

// A post-load hook editing a list or map in place, promoted through an
// unexported squashed struct, is the hook's change: provenance names it,
// as it does for a direct field, rather than the defaults it replaced.
func TestProvenance_InPlaceEditThroughUnexportedSquash(t *testing.T) {
	snap, err := LoadSnapshot[promotedListsCfg](t.Context(),
		WithPostLoad(func(_ context.Context, c *promotedListsCfg) error {
			c.Hosts[0] = "hook"
			c.Labels["team"] = "hook"
			return nil
		}), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, []string{"hook", "b"}, snap.Config.Hosts)
	hook := []Source{{Kind: SourcePostLoad, Name: "0"}}
	assert.Equal(t, hook, snap.Provenance["hosts[0]"].Transforms)
	assert.Equal(t, Source{Kind: SourceTag}, snap.Provenance["hosts[0]"].Input)
	assert.Empty(t, snap.Provenance["hosts[1]"].Transforms)
	assert.Equal(t, hook, snap.Provenance[`labels["team"]`].Transforms)
}

type promotedListsDefaulted struct {
	promotedLists `koanf:",squash"`
}

func (c *promotedListsDefaulted) SetDefaults() {
	c.Hosts[0] = "code"
	c.Labels["team"] = "code"
}

// SetDefaults editing a promoted list or map in place sets the defaults it
// wrote: the value it is compared with afterwards is a copy of its own.
func TestDefaulter_InPlaceEditThroughUnexportedSquash(t *testing.T) {
	got, err := Load[promotedListsDefaulted](t.Context(), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, []string{"code", "b"}, got.Hosts)
	assert.Equal(t, map[string]string{"team": "code"}, got.Labels)
}
