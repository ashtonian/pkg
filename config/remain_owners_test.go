package config

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A sensitive remain field's change is the one Change.Key that holds a
// pattern: it is its Field's Path, ParseKey reads it, it is Within its
// section, and CanonicalKey refuses it as not concrete. A remain field that
// is not sensitive has concrete keys.
func TestDiff_ASensitiveRemainFieldsKeyIsItsPattern(t *testing.T) {
	old := remainDiffCfg{Vault: &remainSecrets{Paths: map[string]string{"prod_db": "s1"}}, Plugins: remainPlugins{Extra: map[string]any{"foo": 1}}}
	next := remainDiffCfg{Vault: &remainSecrets{Paths: map[string]string{"prod_db": "s2"}}, Plugins: remainPlugins{Extra: map[string]any{"foo": 2}}}
	changes, err := Diff(&old, &next)
	require.NoError(t, err)
	require.Equal(t, []string{"plugins.foo", "vault[*]"}, changeKeys(changes))
	assert.Equal(t, "plugins[*]", changes[0].Field.Path, "a pattern only in its Field")
	assert.Equal(t, changes[1].Field.Path, changes[1].Key)

	key, err := ParseKey(changes[1].Key)
	require.NoError(t, err)
	assert.Equal(t, Key{{Kind: SegmentField, Name: "vault"}, {Kind: SegmentAny}}, key)
	assert.Equal(t, "vault[*]", key.String())
	section, err := ParseKey("vault")
	require.NoError(t, err)
	assert.True(t, key.Within(section))
	assert.True(t, key.Within(key))

	_, err = CanonicalKey[remainDiffCfg](changes[1].Key)
	require.ErrorIs(t, err, ErrKey)
	assert.Contains(t, errors.Unwrap(err).Error(), "key must be concrete")
	canonical, err := CanonicalKey[remainDiffCfg](changes[0].Key)
	require.NoError(t, err)
	assert.Equal(t, "plugins.foo", canonical)
}

// An applier may own exactly section[*], the keys a sensitive remain field
// collects, beside one that owns the section's fields: the two own no key
// in common. section[*] lies beneath its section, so the section turning
// nil, with its keys owned by three appliers, waits for a restart whole.
func TestApplier_OwnsExactlyASensitiveRemainField(t *testing.T) {
	src := &remainSource{cfg: remainDiffCfg{Vault: &remainSecrets{Addr: "a", Paths: map[string]string{"prod_db": "s1"}}}}
	set, addr, section := &remainRecorder{}, &remainRecorder{}, &remainRecorder{}
	r := newRemainReloader(t, src, set.applier("set", "vault[*]"), addr.applier("addr", "vault.addr"), section.applier("section", "vault"))

	src.edit(func(c *remainDiffCfg) {
		c.Vault.Addr = "b"
		c.Vault.Paths = map[string]string{"prod_db": "s2"}
	})
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Empty(t, res.Snapshot.PendingRestart)
	require.Len(t, set.calls(), 1)
	assert.Equal(t, []string{"vault[*]"}, changeKeys(set.calls()[0].Changes))
	require.Len(t, addr.calls(), 1)
	assert.Equal(t, []string{"vault.addr"}, changeKeys(addr.calls()[0].Changes))
	assert.Empty(t, section.calls(), "both keys have a more specific owner")

	src.edit(func(c *remainDiffCfg) { c.Vault = nil })
	res, err = r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []string{"vault", "vault.addr", "vault[*]"}, changeKeys(res.Snapshot.PendingRestart))
	assert.Len(t, set.calls(), 1)
	assert.Len(t, addr.calls(), 1)
	assert.Empty(t, section.calls())
}

type liveSecrets struct {
	Addr  string            `koanf:"addr"`
	Paths map[string]string `koanf:",remain" sensitive:"true" reload:"live"`
}

type liveVaultCfg struct {
	Vault liveSecrets `koanf:"vault"`
}

// Under reload tag checks a live sensitive remain field is one live key,
// section[*], which an applier owning it or its section claims; an
// applier owning only the section's restart fields owns no live key.
func TestApplier_ReloadTagChecksClaimASensitiveRemainFieldWhole(t *testing.T) {
	load := func(context.Context) (*liveVaultCfg, error) { return &liveVaultCfg{}, nil }
	apply := func(context.Context, ApplyEvent[liveVaultCfg]) (ApplyResult, error) { return ApplyResult{}, nil }
	build := func(owners ...[]string) error {
		opts := []ReloadOption{WithReloadTagChecks(), WithReloadLogger(discardLogger())}
		for _, prefixes := range owners {
			opts = append(opts, WithApplier(Applier[liveVaultCfg]{Name: prefixes[0], Prefixes: prefixes, Apply: apply}))
		}
		r, err := NewReloaderFunc(t.Context(), load, opts...)
		if err == nil {
			require.NoError(t, r.Stop(t.Context()))
		}
		return err
	}
	err := build()
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "live key vault[*] has no applier")

	assert.NoError(t, build([]string{"vault[*]"}))
	assert.NoError(t, build([]string{"vault"}))

	err = build([]string{"vault[*]"}, []string{"vault.addr"})
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), `applier "vault.addr" owns no live key`)
}

// What an applier defers at section[*] stays owed there when a later
// publication turns the section nil, which is owed at the section's own
// key: two keys, each measured from what was acknowledged, neither lost
// when the other changes.
func TestApplier_DeferredRemainChangeOutlivesItsSectionTurningNil(t *testing.T) {
	src := &remainSource{cfg: remainDiffCfg{Vault: &remainSecrets{Addr: "a", Paths: map[string]string{"prod_db": "s1"}}}}
	rec := &remainRecorder{result: func(ev ApplyEvent[remainDiffCfg]) (ApplyResult, error) {
		return ApplyResult{RestartKeys: changeKeys(ev.Changes)}, nil
	}}
	r := newRemainReloader(t, src, rec.applier("vault", "vault"))
	pending := func() []string {
		t.Helper()
		res, err := r.Reload(t.Context())
		require.NoError(t, err)
		return changeKeys(res.Snapshot.PendingRestart)
	}

	src.edit(func(c *remainDiffCfg) { c.Vault.Paths = map[string]string{"prod_db": "s2"} })
	require.Equal(t, []string{"vault[*]"}, pending())

	src.edit(func(c *remainDiffCfg) { c.Vault = nil })
	assert.Equal(t, []string{"vault", "vault.addr", "vault[*]"}, pending(), "the section and the remain set, under distinct keys")

	src.edit(func(c *remainDiffCfg) { c.Vault = &remainSecrets{Addr: "a", Paths: map[string]string{"prod_db": "s2"}} })
	assert.Equal(t, []string{"vault[*]"}, pending(), "the remain set is still measured from s1")

	src.edit(func(c *remainDiffCfg) { c.Vault = &remainSecrets{Addr: "a", Paths: map[string]string{"prod_db": "s1"}} })
	assert.Empty(t, pending(), "back to what was acknowledged")
}
