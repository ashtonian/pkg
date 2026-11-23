package config

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/knadh/koanf/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rejectedKeys is a WithInvalidEntries report recording the keys it
// withholds in into: an empty slice when it runs with none.
func rejectedKeys(into *[]string) SourceOption {
	return WithInvalidEntries(func(_ context.Context, entries []KeyError) error {
		*into = []string{}
		for _, ke := range entries {
			*into = append(*into, ke.Key)
		}
		return nil
	})
}

type secretDB struct {
	Host     string `koanf:"host" default:"localhost"`
	Password string `koanf:"password" validate:"required"`
}

type secretFilledConfig struct {
	DB        secretDB                    `koanf:"db"`
	Upstreams map[string]secretedUpstream `koanf:"upstreams" validate:"dive"`
}

type secretedUpstream struct {
	URL   string `koanf:"url" validate:"required"`
	Token string `koanf:"token" validate:"required"`
}

// A field the secret populator fills fails every judgement, which runs
// before secrets, but no entry sets it: an entry whose section merges key
// by key and holds nothing at that field is not to blame, and is kept.
func TestJudge_SecretFilledFieldBlamesNoEntry(t *testing.T) {
	secrets := WithSecretPopulator(func(_ context.Context, target any) error {
		c := target.(*secretFilledConfig)
		c.DB.Password = "s3cret"
		for name, u := range c.Upstreams {
			u.Token = "t"
			c.Upstreams[name] = u
		}
		return nil
	})
	var rejected []string
	got, err := Load[secretFilledConfig](t.Context(), secrets,
		WithBaseProvider(mapSource{"upstreams": map[string]any{"a": map[string]any{"url": "https://a.example"}}}, nil, rejectedKeys(&rejected)),
		WithOverrideEntries(readEntries(map[string]any{"db": map[string]any{"host": "prod-db"}}), rejectedKeys(&rejected)))
	require.NoError(t, err)
	assert.Equal(t, "prod-db", got.DB.Host)
	assert.Equal(t, secretedUpstream{URL: "https://a.example", Token: "t"}, got.Upstreams["a"])
	assert.Empty(t, rejected)
}

type tlsPairSection struct {
	CertFile   string `koanf:"cert_file" validate:"required_with=KeyFile"`
	KeyFile    string `koanf:"key_file" validate:"required_with=CertFile"`
	MinVersion int    `koanf:"min_version" default:"12" validate:"ltefield=MaxVersion"`
	MaxVersion int    `koanf:"max_version" default:"13"`
}

type tlsPairConfig struct {
	TLS tlsPairSection `koanf:"tls"`
}

type pairSection struct {
	A  int `koanf:"a"`
	B  int `koanf:"b"`
	Lo int `koanf:"lo" validate:"ltefield=Hi"`
	Hi int `koanf:"hi" default:"10"`
}

// pairConfig's a and b are valid only equal, which only its Validate
// method says.
type pairConfig struct {
	Pair pairSection `koanf:"pair"`
}

func (c *pairConfig) Validate() error {
	if c.Pair.A != c.Pair.B {
		return errors.New("a and b differ")
	}
	return nil
}

type crossStructInner struct {
	Limit int `koanf:"limit" default:"10"`
}

type crossStructConfig struct {
	Inner crossStructInner `koanf:"inner"`
	Value int              `koanf:"value" validate:"ltecsfield=Inner.Limit"`
	Name  string           `koanf:"name"`
}

// A rule comparing fields blames the entries setting the fields it names,
// never the struct holding them: a valid pair beside a sibling that breaks
// such a rule is kept, and so are two entries valid only together. A
// cross-struct rule's param is placed as its key.
func TestJudge_CrossFieldRuleBlamesTheFieldsItNames(t *testing.T) {
	got, rejected, err := judged[tlsPairConfig](t, map[string]any{
		"tls.cert_file": "/etc/tls/crt.pem", "tls.key_file": "/etc/tls/key.pem", "tls.min_version": 99}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"tls.min_version"}, rejected)
	assert.Equal(t, tlsPairSection{CertFile: "/etc/tls/crt.pem", KeyFile: "/etc/tls/key.pem", MinVersion: 12, MaxVersion: 13}, got.TLS)

	pair, rejected, err := judged[pairConfig](t, map[string]any{"pair.a": 3, "pair.b": 3, "pair.lo": 20}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"pair.lo"}, rejected)
	assert.Equal(t, pairSection{A: 3, B: 3, Hi: 10}, pair.Pair)

	cross, rejected, err := judged[crossStructConfig](t, map[string]any{"value": 99, "name": "x"}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"value"}, rejected)
	assert.Equal(t, crossStructConfig{Inner: crossStructInner{Limit: 10}, Name: "x"}, *cross)
}

type shadowedPortConfig struct {
	Port int    `koanf:"port" default:"1" validate:"min=1"`
	Name string `koanf:"name"`
}

// A failure at a key a source not judged sets above the entries blames no
// entry: the entry it sets over is not reported, and the load fails with
// that source's value.
func TestJudge_AFailureAHigherSourceSetsBlamesNoEntry(t *testing.T) {
	var rejected []string
	_, err := Load[shadowedPortConfig](t.Context(),
		WithBaseProvider(mapSource{"port": 8080, "name": "x"}, nil, rejectedKeys(&rejected)),
		WithOverrideProvider(mapSource{"port": 0}, nil))
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "port(min=1)")
	assert.Equal(t, []string{}, rejected)
}

type trioConfig struct {
	A int `koanf:"a"`
	B int `koanf:"b"`
	C int `koanf:"c"`
	D int `koanf:"d"`
}

func (c *trioConfig) Validate() error {
	switch {
	case c.D > 0:
		return errors.New("d is set")
	case c.A > 0 && c.B > 0 && c.C == 0:
		return errors.New("a and b need c")
	}
	return nil
}

// Judging runs no Validate method, so a Validate method's failure an entry
// causes withholds nothing: the load fails with it, as it fails with any
// failure at no key.
func TestJudge_ValidateMethodFailuresWithholdNothing(t *testing.T) {
	_, rejected, err := judged[trioConfig](t, map[string]any{"a": 1, "b": 1, "c": 1, "d": 1}, nil)
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "d is set")
	assert.Empty(t, rejected)

	var reported []string
	stored := map[string]any{"addresses": map[string]any{"first": map[string]any{"port": 2}}, "primary": map[string]any{"port": 6}}
	_, err = Load[editConfig](t.Context(), WithDefaults(editDefaults(false)), WithOverrideProvider(mapSource(stored), nil, rejectedKeys(&reported)))
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "addresses first and second share a port")
	assert.Equal(t, []string{}, reported)
}

// When two judged sources set one key, a failure there is the higher
// one's: it is withheld, and the lower one's value loads.
func TestJudge_TwoJudgedSourcesBlameTheHigher(t *testing.T) {
	var lower, higher []string
	got, err := Load[keyConfig](t.Context(),
		WithBaseProvider(mapSource{"port": 5}, nil, rejectedKeys(&lower)),
		WithOverrideEntries(readEntries(map[string]any{"port": 500}), rejectedKeys(&higher)))
	require.NoError(t, err)
	assert.Equal(t, 5, got.Port)
	assert.Equal(t, []string{}, lower)
	assert.Equal(t, []string{"port"}, higher)
}

// An entry withheld can leave another that needs it unable to apply: the
// next round finds it, and it is withheld too.
func TestJudge_AWithheldEntryExposesTheNext(t *testing.T) {
	type cfg struct {
		Pool []struct {
			Host string `koanf:"host" validate:"required"`
			Port int    `koanf:"port" validate:"max=100"`
		} `koanf:"pool" validate:"dive"`
	}
	got, rejected, err := judged[cfg](t, map[string]any{"pool[0].host": "a", "pool[1]": map[string]any{"host": "b", "port": 500}, "pool[2].host": "c"}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"pool[1]", "pool[2].host"}, rejected)
	require.Len(t, got.Pool, 1)
	assert.Equal(t, "a", got.Pool[0].Host)
}

// countingRules runs the default validator's rules and counts the
// configurations it validates: every judgement, and the load's own.
type countingRules struct{ n *int }

func (c countingRules) Struct(v any) error {
	*c.n++
	return defaultValidator().Struct(v)
}

type labelsConfig struct {
	Labels map[string]string `koanf:"labels"`
	Limit  int               `koanf:"limit" validate:"max=5"`
	Floor  int               `koanf:"floor" validate:"max=5"`
}

// Judging costs at most 2k+1 judgements for k entries ever blamed, however
// many entries there are: one bad entry among a thousand takes three — the
// first judgement, its test beside the rest, and the round that confirms
// it — and two take four.
func TestJudge_JudgementsAreBoundedByTheEntriesBlamed(t *testing.T) {
	for _, tc := range []struct {
		bad  map[string]any
		want int
	}{
		{map[string]any{"limit": 9}, 3},
		{map[string]any{"limit": 9, "floor": 9}, 4},
	} {
		entries := map[string]any{}
		for i := range 1000 {
			entries[fmt.Sprintf(`labels["k%04d"]`, i)] = "v"
		}
		maps.Copy(entries, tc.bad)
		calls := 0
		_, rejected, err := judged[labelsConfig](t, entries, nil, WithValidator(countingRules{&calls}))
		require.NoError(t, err)
		assert.Len(t, rejected, len(tc.bad))
		assert.Equal(t, tc.want, calls-1, "judgements, less the load's own validation")
		assert.LessOrEqual(t, calls-1, 2*len(tc.bad)+1)
	}
}

// Loads sharing one provider's data judge it concurrently without a race:
// each merge works on copies.
func TestJudge_ConcurrentLoadsShareProviderData(t *testing.T) {
	src := mapSource{"pair": map[string]any{"a": 3, "b": 3, "lo": 20}}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			got, err := Load[pairConfig](context.Background(),
				WithBaseProvider(src, nil, WithInvalidEntries(nil)),
				WithOverrideEntries(readEntries(map[string]any{"pair.a": 4, "pair.b": 4})))
			require.NoError(t, err)
			assert.Equal(t, pairSection{A: 4, B: 4, Hi: 10}, got.Pair)
		})
	}
	wg.Wait()
	assert.Equal(t, mapSource{"pair": map[string]any{"a": 3, "b": 3, "lo": 20}}, src)
}

// Entries that break a rule together, each valid beside the entries not
// blamed, are kept and the load fails on the rule: under a file's max of
// 50, an override provider's min of 20 passes beside that max, and its max
// of 15 beside the default min, so neither is withheld.
func TestJudge_EntriesThatConflictAreKeptAndTheLoadFails(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "max: 50\n")
	var rejected []string
	_, err := Load[orderedConfig](t.Context(), WithFile(file),
		WithOverrideProvider(mapSource{"min": 20, "max": 15}, nil, rejectedKeys(&rejected)))
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "min(ltefield=max)")
	assert.Equal(t, []string{}, rejected)
}

type grownServer struct {
	Host string `koanf:"host"`
	Port int    `koanf:"port"`
}

type grownConfig struct {
	Pool      []grownServer          `koanf:"pool" validate:"max=3"`
	Upstreams map[string]grownServer `koanf:"upstreams" validate:"max=2"`
	Name      string                 `koanf:"name"`
}

func grownDefaults() Option {
	return WithDefaults(map[string]any{
		"pool":      []any{map[string]any{"host": "a"}, map[string]any{"host": "b"}},
		"upstreams": map[string]any{"base": map[string]any{"host": "u"}},
	})
}

// A failure at a collection's key — a list's max, a map's len — blames the
// entries beneath it that the merge keeps: stored rows that grow it past
// its rule are judged, each beside the entries not blamed. An entry valid
// there is kept, so rows each valid that break the rule together are kept
// and the load fails on it. Entries a higher source replaces whole are no
// one's to blame.
func TestJudge_AFailureAtACollectionBlamesTheEntriesItHolds(t *testing.T) {
	got, rejected, err := judged[grownConfig](t, map[string]any{"pool[2].host": "c", "pool[3].host": "d", "name": "x"}, []Option{grownDefaults()})
	require.NoError(t, err)
	assert.Equal(t, []string{"pool[3].host"}, rejected)
	assert.Equal(t, []grownServer{{Host: "a"}, {Host: "b"}, {Host: "c"}}, got.Pool)
	assert.Equal(t, "x", got.Name)

	_, rejected, err = judged[grownConfig](t, map[string]any{`upstreams["x"].host`: "x", `upstreams["y"].host`: "y"}, []Option{grownDefaults()})
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "upstreams(max")
	assert.Empty(t, rejected)

	four := []any{map[string]any{"host": "1"}, map[string]any{"host": "2"}, map[string]any{"host": "3"}, map[string]any{"host": "4"}}
	_, rejected, err = judged[grownConfig](t, map[string]any{"pool[2].host": "c"}, []Option{grownDefaults()},
		WithOverrideProvider(mapSource{"pool": four}, nil))
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "pool(max")
	assert.Empty(t, rejected)
}

// A listEdit merges into the list beneath it element by element, as
// mergeValue merges it: the spine gives an element it sets, or appends, to
// the edit's owner and leaves the rest to what set them, and over no list it
// makes a list of the edit's elements alone. What the spine holds is what
// mergeValue's merge holds. Each edit's indexes run on from the list's end,
// as the edit tree makes them.
func TestJudge_SpineMergesAListEditByElement(t *testing.T) {
	layer := entryRef{layer: 0, entry: -1}
	beneath := beneathLayers()
	onTwo := map[string]any{"pool": listEdit{1: map[string]any{"port": 2}, 2: map[string]any{"host": "c"}}}
	fresh := map[string]any{"pool": listEdit{0: map[string]any{"port": 2}, 1: map[string]any{"host": "c"}}}
	for _, tt := range []struct {
		name       string
		base, edit map[string]any
		want       map[string]entryRef
	}{
		{
			name: "over a list",
			base: map[string]any{"pool": []any{map[string]any{"host": "a", "port": 1}, map[string]any{"host": "b"}}},
			edit: onTwo,
			want: map[string]entryRef{
				"pool": layer, "pool[0].host": beneath, "pool[0].port": beneath,
				"pool[1].host": beneath, "pool[1].port": layer, "pool[2].host": layer, "pool[2].port": beneath,
			},
		},
		{
			name: "over a value that is no list",
			base: map[string]any{"pool": map[string]any{"host": "a"}},
			edit: fresh,
			want: map[string]entryRef{"pool": layer, "pool[0].host": beneath, "pool[0].port": layer, "pool[1].host": layer, "pool[2].host": beneath},
		},
		{
			name: "over nothing",
			base: map[string]any{},
			edit: fresh,
			want: map[string]entryRef{"pool": layer, "pool[0].host": beneath, "pool[0].port": layer, "pool[1].host": layer},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			merged := deepCopyMap(tt.base)
			mergeTrees(deepCopyMap(tt.edit), merged)
			for key, want := range tt.want {
				p, err := parseKeyPath(key)
				require.NoError(t, err)
				s := newSpine(p, tt.base)
				s.apply(0, tt.edit, layer)
				node := s.nodes[len(p)]
				assert.Equal(t, want, node.owner, key)
				assert.Equal(t, holdsRaw(merged, p.raw()), node.present, "%s: present as the merge holds it", key)
			}
		})
	}
}

type provSection struct {
	Host string `koanf:"host"`
	Port int    `koanf:"port"`
}

type provConfig struct {
	Name    string                 `koanf:"name"`
	DB      provSection            `koanf:"db"`
	Servers map[string]provSection `koanf:"servers"`
	Pool    []provSection          `koanf:"pool"`
	Tags    map[string]string      `koanf:"tags"`
}

// The judge's setter names, for every key of a load, the source its
// provenance names: what merges key by key, a typed section, a typed map,
// an entries source's index and map-key entries, a provider judged entry by
// entry, and a higher source replacing them whole.
func TestJudge_SetterAgreesWithProvenance(t *testing.T) {
	defaults := WithDefaults(map[string]any{
		"pool":    []any{map[string]any{"host": "d0", "port": 1}, map[string]any{"host": "d1"}},
		"servers": map[string]any{"a": map[string]any{"host": "da"}},
	})
	entries := map[string]any{
		"pool[1].host": "B", "pool[2]": map[string]any{"host": "C"},
		`servers["a"].port`: 7, `servers["b"]`: map[string]any{"host": "b"}, "name": "n",
	}
	for name, opts := range map[string][]Option{
		"maps merging key by key": {
			WithBaseProvider(mapSource{"name": "a", "db": map[string]any{"host": "h"}, "tags": map[string]any{"x": "1"}}, nil),
			WithOverrideProvider(mapSource{"db": map[string]any{"port": 5}, "tags": map[string]any{"y": "2"}}, nil),
		},
		"a typed section": {
			WithBaseProvider(mapSource{"db": map[string]any{"host": "h", "port": 1}, "name": "a"}, nil),
			WithOverrideProvider(mapSource{"db": provSection{Host: "t"}}, nil),
		},
		"a typed map": {
			defaults,
			WithBaseProvider(mapSource{"servers": map[string]any{"b": map[string]any{"host": "b", "port": 2}}}, nil),
			WithOverrideProvider(mapSource{"servers": map[string]provSection{"c": {Host: "c"}}}, nil),
		},
		"entries with index and map-key entries": {
			defaults,
			WithBaseProvider(mapSource{"servers": map[string]any{"a": map[string]any{"host": "a"}}, "db": map[string]any{"host": "h"}}, nil),
			WithOverrideEntries(readEntries(entries), WithInvalidEntries(nil)),
		},
		"a provider judged entry by entry": {
			defaults,
			WithBaseProvider(mapSource{"db": map[string]any{"host": "h"}, "servers": map[string]any{"b": map[string]any{"host": "b"}}, "tags": map[string]any{"x": "1"}}, nil, WithInvalidEntries(nil)),
			WithOverrideProvider(mapSource{"db": map[string]any{"port": 5}, "tags": map[string]any{"y": "2"}}, nil),
		},
		"a higher source replacing entries whole": {
			defaults,
			WithBaseEntries(readEntries(entries), WithInvalidEntries(nil)),
			WithOverrideProvider(mapSource{"pool": []any{map[string]any{"host": "r"}}, "servers": map[string]provSection{"z": {Host: "z"}}}, nil),
		},
	} {
		t.Run(name, func(t *testing.T) {
			snap, err := LoadSnapshot[provConfig](t.Context(), opts...)
			require.NoError(t, err)

			o := buildOptions(opts)
			root := reflect.TypeFor[provConfig]()
			schema, err := loadSchema(root, o)
			require.NoError(t, err)
			l := &loader{k: koanf.New("."), root: root, schema: schema}
			_, err = l.addSchemaDefaults(o)
			require.NoError(t, err)
			if len(o.defaults) > 0 {
				require.NoError(t, l.addFlat("WithDefaults", o.defaults, constant(Source{Kind: SourceDefaults})))
			}
			layers, _, err := l.readLayers(t.Context(), o)
			require.NoError(t, err)
			j := newJudge(t.Context(), layers)
			j.from, j.base = 0, l.k.Raw()
			all := make([]bool, len(j.candidates))
			for c := range all {
				all[c] = true
			}
			for _, y := range layers {
				_, refused, err := l.mergeLayer(y, y.entries)
				require.NoError(t, err)
				require.Empty(t, refused)
			}
			merged := l.k.Raw()

			require.NotEmpty(t, snap.Provenance)
			for key, origin := range snap.Provenance {
				p, err := parseKeyPath(key)
				require.NoError(t, err, key)
				by := j.setter(p, all, nil)
				unset := origin.Input.Kind == SourceZero || origin.Input.Kind == SourceTag
				switch {
				case by.layer < 0:
					assert.True(t, unset || origin.Input.Kind == SourceDefaults, "%s: %v", key, origin.Input)
				case unset:
					// No source holds a value there; the setter names the
					// source that replaced a key holding one.
					assert.False(t, holdsRaw(merged, p.raw()), "%s: %v", key, origin.Input)
				default:
					assert.Equal(t, origin.Input, layers[by.layer].label.at(p.raw()), key)
				}
			}
		})
	}
}
