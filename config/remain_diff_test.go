package config

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/knadh/koanf/providers/confmap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/config/internal/deepcopy"
)

type remainSecrets struct {
	Addr  string            `koanf:"addr"`
	Paths map[string]string `koanf:",remain" sensitive:"true"`
}

type remainDiffCfg struct {
	Plugins remainPlugins  `koanf:"plugins"`
	Vault   *remainSecrets `koanf:"vault"`
	Typed   remainTyped    `koanf:"typed"`
	Locked  remainLabels   `koanf:"locked" sensitive:"true"`
	Ext     any            `koanf:"ext"`
}

// A key a remain field collects is compared as a key of its section, after
// the section's fields and by name, owned by the remain field.
func TestDiff_ComparesTheKeysARemainFieldCollects(t *testing.T) {
	old := &remainDiffCfg{
		Plugins: remainPlugins{Host: "h", Extra: map[string]any{"foo": 1, "gone": "x", "api_token": "t1"}},
		Typed:   remainTyped{Items: map[string]remainItem{"x": {Port: 1}}},
	}
	next := &remainDiffCfg{
		Plugins: remainPlugins{Host: "h", Extra: map[string]any{"foo": 2, "added": true, "api_token": "t2"}},
		Typed:   remainTyped{Items: map[string]remainItem{"x": {Port: 2}, "y": {Port: 3}}},
	}
	changes, err := Diff(old, next)
	require.NoError(t, err)
	require.Equal(t, []string{"plugins.added", "plugins.api_token", "plugins.foo", "plugins.gone", "typed.x.port", "typed.y", "typed.y.port"}, changeKeys(changes))

	added, token, foo, gone := changes[0], changes[1], changes[2], changes[3]
	assert.Equal(t, Value{}, added.Old)
	assert.Equal(t, Value{Present: true, Type: "bool", Text: "true"}, added.New)
	assert.Equal(t, "plugins[*]", added.Field.Path)
	assert.Equal(t, ReloadLive, added.Field.Reload)
	assert.Equal(t, redactedValue, token.Old.Text, "a key named like a credential")
	assert.Equal(t, redactedValue, token.New.Text)
	assert.True(t, token.Field.Sensitive)
	assert.Equal(t, "1", foo.Old.Text)
	assert.Equal(t, "2", foo.New.Text)
	assert.False(t, gone.New.Present)
	assert.Equal(t, "typed[*].port", changes[4].Field.Path)
	assert.True(t, changes[5].Structural)

	same, err := Diff(next, next)
	require.NoError(t, err)
	assert.Empty(t, same)
}

// A sensitive remain field is compared whole, as a sensitive map is: one
// change at section[*], redacted, so no key it holds is listed.
func TestDiff_ComparesASensitiveRemainFieldWhole(t *testing.T) {
	vault := func(paths map[string]string) *remainDiffCfg {
		return &remainDiffCfg{Vault: &remainSecrets{Addr: "a", Paths: paths}}
	}
	changes, err := Diff(vault(map[string]string{"prod_db": "s1"}), vault(map[string]string{"prod_db": "s2", "kv": "s3"}))
	require.NoError(t, err)
	require.Equal(t, []string{"vault[*]"}, changeKeys(changes))
	c := changes[0]
	assert.False(t, c.Structural)
	assert.Equal(t, Value{Present: true, Type: "map[string]string", Text: redactedValue, Redacted: true}, c.Old)
	assert.Equal(t, Value{Present: true, Type: "map[string]string", Text: redactedValue, Redacted: true}, c.New)
	assert.Equal(t, "vault[*]", c.Field.Path)
	assert.True(t, c.Field.Sensitive)
	assert.Equal(t, reflect.TypeFor[map[string]string](), c.Field.Type)

	changes, err = Diff(vault(nil), vault(map[string]string{}))
	require.NoError(t, err)
	assert.Empty(t, changes, "a remain field collecting nothing has no value, nil or empty")

	changes, err = Diff(&remainDiffCfg{}, vault(map[string]string{"prod_db": "s1"}))
	require.NoError(t, err)
	require.Equal(t, []string{"vault", "vault.addr", "vault[*]"}, changeKeys(changes))
	assert.True(t, changes[2].Structural)
	assert.False(t, changes[2].Old.Present)
	assert.Equal(t, redactedValue, changes[2].New.Text)

	// A remain field beneath a sensitive section is sensitive too.
	changes, err = Diff(
		&remainDiffCfg{Locked: remainLabels{Team: "a", Values: map[string]string{"k": "one"}}},
		&remainDiffCfg{Locked: remainLabels{Team: "b", Values: map[string]string{"k": "two"}}},
	)
	require.NoError(t, err)
	require.Equal(t, []string{"locked.team", "locked[*]"}, changeKeys(changes))
	assert.Equal(t, redactedValue, changes[1].New.Text)
	assert.Equal(t, "locked[*]", changes[1].Field.Path)

	f, err := FieldFor[remainDiffCfg]("vault[*]")
	require.NoError(t, err)
	assert.Equal(t, c.Field, f, "the change's key resolves to its field")
}

// remainSource hands a reloader fresh copies of a config the test edits.
type remainSource struct {
	mu  sync.Mutex
	cfg remainDiffCfg
}

func (s *remainSource) edit(fn func(*remainDiffCfg)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.cfg)
}

func (s *remainSource) load(context.Context) (*remainDiffCfg, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := deepcopy.Value(reflect.ValueOf(s.cfg)).Interface().(remainDiffCfg)
	return &c, nil
}

// remainRecorder is an applier that records its events and answers with
// result.
type remainRecorder struct {
	mu     sync.Mutex
	events []ApplyEvent[remainDiffCfg]
	result func(ApplyEvent[remainDiffCfg]) (ApplyResult, error)
}

func (r *remainRecorder) apply(_ context.Context, ev ApplyEvent[remainDiffCfg]) (ApplyResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	if r.result != nil {
		return r.result(ev)
	}
	return ApplyResult{}, nil
}

func (r *remainRecorder) calls() []ApplyEvent[remainDiffCfg] {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ApplyEvent[remainDiffCfg](nil), r.events...)
}

func (r *remainRecorder) applier(name string, prefixes ...string) ReloadOption {
	return WithApplier(Applier[remainDiffCfg]{Name: name, Prefixes: prefixes, Apply: r.apply})
}

func newRemainReloader(t *testing.T, src *remainSource, opts ...ReloadOption) *Reloader[remainDiffCfg] {
	t.Helper()
	r, err := NewReloaderFunc(t.Context(), src.load, append(opts, WithReloadLogger(discardLogger()))...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop(context.Background()) })
	return r
}

// A key a remain field collects routes by the remain field's reload tag to
// the applier whose prefix names it as Diff does, or its section.
func TestApplier_RoutesTheKeysARemainFieldCollects(t *testing.T) {
	src := &remainSource{}
	section, foo := &remainRecorder{}, &remainRecorder{}
	r := newRemainReloader(t, src, WithReloadTagChecks(), section.applier("plugins", "plugins"), foo.applier("foo", `plugins["foo"]`))

	src.edit(func(c *remainDiffCfg) {
		c.Plugins.Host = "h2"
		c.Plugins.Extra = map[string]any{"foo": 1, "bar": 2}
	})
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"plugins.host"}, changeKeys(res.Snapshot.PendingRestart), "the field's own class")
	require.Len(t, section.calls(), 1)
	assert.Equal(t, []string{"plugins.bar"}, changeKeys(section.calls()[0].Changes))
	require.Len(t, foo.calls(), 1)
	assert.Equal(t, []string{"plugins.foo"}, changeKeys(foo.calls()[0].Changes))
	assert.Equal(t, ReloadLive, foo.calls()[0].Changes[0].Field.Reload)
}

// A live remain field's keys need an applier under reload tag checks, and
// an applier that owns them owns live keys.
func TestApplier_ReloadTagChecksSeeARemainField(t *testing.T) {
	load := (&remainSource{}).load
	_, err := NewReloaderFunc(t.Context(), load, WithReloadTagChecks(), WithReloadLogger(discardLogger()))
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "live key plugins[*] has no applier")

	apply := func(context.Context, ApplyEvent[remainDiffCfg]) (ApplyResult, error) { return ApplyResult{}, nil }
	r, err := NewReloaderFunc(t.Context(), load, WithReloadTagChecks(), WithReloadLogger(discardLogger()),
		WithApplier(Applier[remainDiffCfg]{Name: "one", Prefixes: []string{"plugins.foo"}, Apply: apply}),
		WithApplier(Applier[remainDiffCfg]{Name: "rest", Prefixes: []string{"plugins"}, Apply: apply}))
	require.NoError(t, err)
	require.NoError(t, r.Stop(t.Context()))
}

// A prefix names a key a remain field collects as Diff does, and the keys a
// sensitive remain field collects as Diff does, whole, at section[*]: one
// naming a key it collects, or anything beneath section[*], reaches beneath
// a value compared whole. [*] on any other struct is no prefix.
func TestApplier_PrefixesAndTheKeysARemainFieldCollects(t *testing.T) {
	load := (&remainSource{}).load
	apply := func(context.Context, ApplyEvent[remainDiffCfg]) (ApplyResult, error) { return ApplyResult{}, nil }
	build := func(prefixes ...string) error {
		r, err := NewReloaderFunc(t.Context(), load, WithReloadLogger(discardLogger()), WithApplier(Applier[remainDiffCfg]{Name: "a", Prefixes: prefixes, Apply: apply}))
		if err == nil {
			require.NoError(t, r.Stop(t.Context()))
		}
		return err
	}
	for _, ok := range []string{"plugins.foo", `plugins["my-plugin"]`, "typed.x.port", "vault", "vault[*]", "locked[*]"} {
		assert.NoError(t, build(ok), ok)
	}
	for _, tt := range []struct{ prefix, msg string }{
		{"vault.prod_db", "it reaches beneath vault[*], which is sensitive and compared whole"},
		{`vault["prod_db"]`, "it reaches beneath vault[*], which is sensitive and compared whole"},
		{"locked.k", "it reaches beneath locked[*], which is sensitive and compared whole"},
		{"plugins.api_token.x", "it reaches beneath plugins.api_token, which is sensitive and compared whole"},
		{"vault[*].x", "it reaches beneath vault[*], which is sensitive and compared whole"},
		{`vault[*]["x"]`, "it reaches beneath vault[*], which is sensitive and compared whole"},
		{"plugins[*]", "plugins compares the keys its remain field collects one by one; own plugins, or name one of them"},
		{"typed[*].port", "typed compares the keys its remain field collects one by one"},
		{"plugins.foo[*]", "reaches beneath plugins.foo"},
		{"typed.x[*]", "typed.x is a struct; own it, or name a key it holds"},
	} {
		err := build(tt.prefix)
		require.ErrorIs(t, err, ErrKey, tt.prefix)
		assert.Contains(t, err.Error(), tt.msg, tt.prefix)
	}
	err := build("plugins.foo", `plugins["foo"]`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lists plugins.foo twice", "spelled one way")
}

// A sensitive remain field is one change, at section[*], acknowledged,
// deferred and measured like any key compared whole.
func TestApplier_ASensitiveRemainFieldIsOneChange(t *testing.T) {
	src := &remainSource{cfg: remainDiffCfg{Vault: &remainSecrets{Addr: "a", Paths: map[string]string{"prod_db": "s1"}}}}
	rec := &remainRecorder{result: func(ev ApplyEvent[remainDiffCfg]) (ApplyResult, error) {
		return ApplyResult{RestartKeys: changeKeys(ev.Changes)}, nil
	}}
	r := newRemainReloader(t, src, rec.applier("vault", "vault"))

	src.edit(func(c *remainDiffCfg) { c.Vault.Paths = map[string]string{"prod_db": "s2"} })
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	require.Len(t, rec.calls(), 1)
	got := rec.calls()[0].Changes
	require.Equal(t, []string{"vault[*]"}, changeKeys(got))
	assert.Equal(t, redactedValue, got[0].Old.Text)
	assert.Equal(t, "vault[*]", got[0].Field.Path)
	assert.Equal(t, []string{"vault[*]"}, changeKeys(res.Snapshot.PendingRestart))

	src.edit(func(c *remainDiffCfg) { c.Vault.Paths = map[string]string{"prod_db": "s3"} })
	res, err = r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"vault[*]"}, changeKeys(res.Snapshot.PendingRestart), "still owed, measured from s1")

	src.edit(func(c *remainDiffCfg) { c.Vault.Paths = map[string]string{"prod_db": "s1"} })
	res, err = r.Reload(t.Context())
	require.NoError(t, err)
	assert.Empty(t, res.Snapshot.PendingRestart, "back to what was acknowledged")
	assert.Len(t, rec.calls(), 2)
}

// What an applier acknowledged at a remain field's keys is kept when an
// interface holding the section changes type over them, so a return to it
// owes nothing.
func TestApplier_InterfaceTypeChangesOverDeferredRemainKeys(t *testing.T) {
	setup := func(t *testing.T) (*remainSource, *remainRecorder, *Reloader[remainDiffCfg]) {
		src := &remainSource{cfg: remainDiffCfg{Ext: remainSecrets{Addr: "a", Paths: map[string]string{"db": "s1"}}}}
		rec := &remainRecorder{result: func(ev ApplyEvent[remainDiffCfg]) (ApplyResult, error) {
			return ApplyResult{RestartKeys: changeKeys(ev.Changes)}, nil
		}}
		r := newRemainReloader(t, src, rec.applier("ext", "ext"))
		src.edit(func(c *remainDiffCfg) { c.Ext = remainSecrets{Addr: "a", Paths: map[string]string{"db": "s2"}} })
		res, err := r.Reload(t.Context())
		require.NoError(t, err)
		require.Equal(t, []string{"ext[*]"}, changeKeys(res.Snapshot.PendingRestart))

		src.edit(func(c *remainDiffCfg) { c.Ext = "text" })
		res, err = r.Reload(t.Context())
		require.NoError(t, err)
		require.Equal(t, []string{"ext"}, changeKeys(res.Snapshot.PendingRestart))
		return src, rec, r
	}
	t.Run("back to what was acknowledged", func(t *testing.T) {
		src, _, r := setup(t)
		src.edit(func(c *remainDiffCfg) { c.Ext = remainSecrets{Addr: "a", Paths: map[string]string{"db": "s1"}} })
		res, err := r.Reload(t.Context())
		require.NoError(t, err)
		assert.Empty(t, res.Snapshot.PendingRestart)
	})
	t.Run("back to the section, one field different", func(t *testing.T) {
		src, rec, r := setup(t)
		src.edit(func(c *remainDiffCfg) { c.Ext = remainSecrets{Addr: "b", Paths: map[string]string{"db": "s1"}} })
		_, err := r.Reload(t.Context())
		require.NoError(t, err)
		calls := rec.calls()
		assert.Equal(t, []string{"ext.addr"}, changeKeys(calls[len(calls)-1].Changes), "measured from what was acknowledged")
	})
}

// Provenance and PrintExplain attribute each key a remain field collects,
// and a sensitive remain field whole.
func TestProvenance_AttributesTheKeysARemainFieldCollects(t *testing.T) {
	data := map[string]any{
		"plugins": map[string]any{"foo": 1},
		"vault":   map[string]any{"addr": "a", "prod_db": "s1"},
	}
	snap, err := LoadSnapshot[remainDiffCfg](t.Context(), WithBaseProvider(confmap.Provider(data, ""), nil), WithSkipValidation())
	require.NoError(t, err)
	for _, key := range []string{"plugins.foo", "vault[*]"} {
		o, ok := snap.Provenance[key]
		require.True(t, ok, key)
		assert.Equal(t, SourceBase, o.Input.Kind, key)
		assert.True(t, o.Exact, key)
	}
	assert.NotContains(t, snap.Provenance, "vault.prod_db")

	var out bytes.Buffer
	require.NoError(t, PrintExplain(snap, WithWriter(&out)))
	assert.Contains(t, out.String(), "plugins.foo")
	assert.NotContains(t, out.String(), "prod_db")
	assert.NotContains(t, out.String(), "s1")
}

// The koanf:"-" fields of a typed section a remain field collects are kept
// through the merge, as any section's are.
func TestLoad_KeepsTheUnkeyedFieldsOfASectionARemainFieldCollects(t *testing.T) {
	type item struct {
		Port  int    `koanf:"port"`
		State string `koanf:"-"`
	}
	type cfg struct {
		Items map[string]item `koanf:",remain"`
	}
	got, err := Load[cfg](t.Context(), WithDefaults(map[string]any{"x": item{Port: 1, State: "kept"}}))
	require.NoError(t, err)
	assert.Equal(t, item{Port: 1, State: "kept"}, got.Items["x"])
}

// A remain key a higher source also sets is a shadowed input at its key; one
// a sensitive remain field collects is named only by its section.
func TestShadow_NamesTheKeysARemainFieldCollects(t *testing.T) {
	low := map[string]any{"plugins": map[string]any{"foo": 1}, "vault": map[string]any{"prod_db": "s1"}}
	high := map[string]any{"plugins": map[string]any{"foo": 2}, "vault": map[string]any{"prod_db": "s2"}}
	snap, err := LoadSnapshot[remainDiffCfg](t.Context(),
		WithBaseProvider(confmap.Provider(low, ""), nil),
		WithOverrideProvider(confmap.Provider(high, ""), nil),
		WithSkipValidation())
	require.NoError(t, err)
	var keys []string
	for _, s := range snap.Shadowed {
		keys = append(keys, s.Key)
	}
	assert.Equal(t, []string{"plugins.foo", "vault"}, keys)
	assert.NotContains(t, strings.Join(keys, " "), "prod_db")
}
