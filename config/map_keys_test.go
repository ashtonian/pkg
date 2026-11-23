package config

import (
	"fmt"
	"log/slog"
	"net/netip"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mkColor string

type mkLevel int

// mkNamedLevel prints as a name, but decodes from and is keyed by its number.
type mkNamedLevel int

func (l mkNamedLevel) String() string { return [...]string{"debug", "info", "warn"}[l] }

// mkTextLevel is keyed by the text its methods write and read.
type mkTextLevel int

func (l mkTextLevel) MarshalText() ([]byte, error) { return []byte(fmt.Sprintf("lvl-%d", int(l))), nil }

func (l *mkTextLevel) UnmarshalText(b []byte) error {
	n, ok := strings.CutPrefix(string(b), "lvl-")
	if !ok {
		return fmt.Errorf("level %q has no lvl- prefix", b)
	}
	_, err := fmt.Sscan(n, (*int)(l))
	return err
}

type mkServer struct {
	Host string `koanf:"host" alias:"hostname"`
	Port int    `koanf:"port"`
	Hook string `koanf:"-"`
}

type mkConfig struct {
	ByColor map[mkColor]mkServer      `koanf:"by_color"`
	ByLevel map[mkLevel]mkServer      `koanf:"by_level"`
	ByName  map[string]mkServer       `koanf:"by_name"`
	ByInt   map[int]mkServer          `koanf:"by_int"`
	ByUint  map[uint8]string          `koanf:"by_uint"`
	ByBool  map[bool]int              `koanf:"by_bool"`
	ByFloat map[float64]string        `koanf:"by_float"`
	ByDur   map[time.Duration]string  `koanf:"by_dur"`
	BySlog  map[slog.Level]string     `koanf:"by_slog"`
	ByText  map[mkTextLevel]mkServer  `koanf:"by_text"`
	ByNamed map[mkNamedLevel]mkServer `koanf:"by_named"`
	ByAddr  map[netip.Addr]string     `koanf:"by_addr"`
	Route   netip.Prefix              `koanf:"route"`
	Extra   any                       `koanf:"extra"`
	Open    struct {
		Rest map[string]any `koanf:",remain"`
	} `koanf:"open"`
	Arr   [2]mkServer `koanf:"arr"`
	Other int         `koanf:"other"`
}

// A typed section in a map keyed by any type keeps its koanf:"-" fields
// through a load, DecodeKey and DecodeEdits, as one in a map keyed by
// strings or in an array does: the entry is named, and found again, by its
// key's text.
func TestMapKeys_SectionsKeepUnkeyedFieldsWhateverTheKey(t *testing.T) {
	defaults := WithDefaults(map[string]any{
		"by_color": map[mkColor]mkServer{"red": {Host: "r", Hook: "hook red"}},
		"by_level": map[mkLevel]mkServer{3: {Host: "l", Hook: "hook level"}},
		"by_name":  map[string]mkServer{"n": {Host: "n", Hook: "hook name"}},
		"by_text":  map[mkTextLevel]mkServer{2: {Host: "t", Hook: "hook text"}},
		"by_named": map[mkNamedLevel]mkServer{1: {Host: "i", Hook: "hook named"}},
		"arr":      [2]mkServer{{Host: "a0", Hook: "hook a0"}},
	})
	check := func(t *testing.T, got *mkConfig) {
		t.Helper()
		assert.Equal(t, "hook name", got.ByName["n"].Hook)
		assert.Equal(t, "hook a0", got.Arr[0].Hook)
		assert.Equal(t, "hook red", got.ByColor["red"].Hook)
		assert.Equal(t, "hook level", got.ByLevel[3].Hook)
		assert.Equal(t, "hook text", got.ByText[2].Hook)
		assert.Equal(t, "hook named", got.ByNamed[1].Hook)
	}
	t.Run("load", func(t *testing.T) {
		got, err := Load[mkConfig](t.Context(), defaults)
		require.NoError(t, err)
		check(t, got)
	})
	t.Run("DecodeKey", func(t *testing.T) {
		got, err := DecodeKey[mkConfig](t.Context(), "other", 1, defaults)
		require.NoError(t, err)
		check(t, got)
	})
	t.Run("DecodeEdits over a baseline", func(t *testing.T) {
		base := mkConfig{
			ByColor: map[mkColor]mkServer{"red": {Host: "r", Hook: "hook red"}},
			ByLevel: map[mkLevel]mkServer{3: {Host: "l", Hook: "hook level"}},
			ByName:  map[string]mkServer{"n": {Host: "n", Hook: "hook name"}},
			ByText:  map[mkTextLevel]mkServer{2: {Host: "t", Hook: "hook text"}},
			ByNamed: map[mkNamedLevel]mkServer{1: {Host: "i", Hook: "hook named"}},
			Arr:     [2]mkServer{{Host: "a0", Hook: "hook a0"}},
		}
		got, err := DecodeEdits(t.Context(), &base, map[string]any{"other": 1})
		require.NoError(t, err)
		check(t, got)
		want := base
		want.Other = 1
		assert.Equal(t, want, *got, "the candidate is the baseline with the one edit")
	})
	t.Run("a higher source replacing the map drops them", func(t *testing.T) {
		// A map keyed by a named type is replaced whole by a higher source,
		// so the section a file sets keys of is the file's alone.
		file := filepath.Join(t.TempDir(), "c.yaml")
		writeFile(t, file, "by_level: {3: {port: 9}}\nby_name: {n: {port: 9}}\n")
		got, err := Load[mkConfig](t.Context(), defaults, WithFile(file))
		require.NoError(t, err)
		assert.Equal(t, mkServer{Port: 9}, got.ByLevel[3])
		assert.Equal(t, mkServer{Host: "n", Port: 9, Hook: "hook name"}, got.ByName["n"], "a map keyed by strings merges by key")
	})
	t.Run("a typed section set at an entry", func(t *testing.T) {
		got, err := DecodeKey[mkConfig](t.Context(), `by_level["3"]`, mkServer{Host: "x", Hook: "hook set"})
		require.NoError(t, err)
		assert.Equal(t, mkServer{Host: "x", Hook: "hook set"}, got.ByLevel[3])
		got, err = DecodeKey[mkConfig](t.Context(), `by_text["lvl-2"]`, mkServer{Host: "y", Hook: "hook set"})
		require.NoError(t, err)
		assert.Equal(t, mkServer{Host: "y", Hook: "hook set"}, got.ByText[2])
	})
}

// A key beneath a map entry is spelled one way whatever the map's key type:
// the entry by its key's text as the decoder reads it, and what lies
// beneath by the element's schema, aliases and letter case included. A
// field segment on a map, and a key beneath a value with none — a scalar,
// a struct that decodes from text — are refused; an interface or remain
// field still owns what lies beneath it.
func TestMapKeys_CanonicalKey(t *testing.T) {
	for _, tc := range []struct{ key, want string }{
		{`by_level["3"].HOST`, `by_level["3"].host`},
		{`by_level["3"].hostname`, `by_level["3"].host`},
		{`by_level["03"].port`, `by_level["3"].port`},
		{`by_color["red"].HOSTNAME`, `by_color["red"].host`},
		{`by_int["3"].hostname`, `by_int["3"].host`},
		{`by_int["010"]`, `by_int["8"]`},
		{`by_int["0x10"].port`, `by_int["16"].port`},
		{`by_int["+5"]`, `by_int["5"]`},
		{`by_int["-0"]`, `by_int["0"]`},
		{`by_uint["0b11"]`, `by_uint["3"]`},
		{`by_bool["TRUE"]`, `by_bool["true"]`},
		{`by_bool["0"]`, `by_bool["false"]`},
		{`by_float["1.50"]`, `by_float["1.5"]`},
		{`by_dur["60s"]`, `by_dur["1m0s"]`},
		{`by_slog["warn"]`, `by_slog["WARN"]`},
		{`by_text["lvl-02"].host`, `by_text["lvl-2"].host`},
		{`by_named["1"].host`, `by_named["1"].host`},
		{`by_addr["192.0.2.1"]`, `by_addr["192.0.2.1"]`},
		{`extra.a["b"].c`, "extra.a.b.c"},
		{`open["x"].y`, "open.x.y"},
	} {
		got, err := CanonicalKey[mkConfig](tc.key)
		if assert.NoError(t, err, tc.key) {
			assert.Equal(t, tc.want, got, tc.key)
		}
	}
	for _, key := range []string{
		"by_level.foo",
		`by_level["3"]["x"]["y"]`,
		`by_level["x"]`,
		`by_int["080"]`,
		`by_int["1.5"]`,
		`by_uint["256"]`,
		`by_bool["yes"]`,
		`by_bool["true"].x`,
		`by_dur["60"]`,
		`by_text["2"]`,
		`by_named["info"]`,
		`by_addr["nope"]`,
		"route.bits",
		`route["x"][0]`,
	} {
		_, err := CanonicalKey[mkConfig](key)
		var ke *KeyError
		if assert.ErrorAs(t, err, &ke, key) {
			assert.True(t, ke.Unknown, key)
			assert.ErrorIs(t, err, ErrKey, key)
		}
	}
}

// A key set under two spellings of one entry is one key set twice, and a
// key written beneath a map entry — a stored entry, DecodeKey — loads as
// the same key a file writes there, an alias included.
func TestMapKeys_OneEntryOneKey(t *testing.T) {
	for _, edits := range []map[string]any{
		{`by_level["3"].HOST`: "a", `by_level["3"].host`: "b"},
		{`by_level["3"].hostname`: "a", `by_level["03"].host`: "b"},
		{`by_int["010"]`: map[string]any{}, `by_int["8"]`: map[string]any{}},
		{`by_bool["1"]`: 1, `by_bool["true"]`: 2},
	} {
		_, err := DecodeEdits[mkConfig](t.Context(), nil, edits)
		var ee *EditsError
		if assert.ErrorAs(t, err, &ee, "%v", edits) && assert.Len(t, ee.Refused, 1, "%v", edits) {
			assert.Contains(t, ee.Refused[0].Error(), "the key is set more than once, under different spellings")
		}
	}

	file := filepath.Join(t.TempDir(), "c.yaml")
	writeFile(t, file, "by_level: {3: {hostname: h}}\nby_int: {\"010\": {host: o}}\n")
	fromFile, err := Load[mkConfig](t.Context(), WithFile(file))
	require.NoError(t, err)
	assert.Equal(t, "h", fromFile.ByLevel[3].Host)
	assert.Equal(t, "o", fromFile.ByInt[8].Host, "the decoder reads 010 as octal")

	written := map[string]any{`by_level["3"].hostname`: "h", `by_int["010"].host`: "o"}
	stored, err := Load[mkConfig](t.Context(), WithOverrideEntries(readEntries(written)))
	require.NoError(t, err)
	assert.Equal(t, fromFile.ByLevel, stored.ByLevel)
	assert.Equal(t, fromFile.ByInt, stored.ByInt)

	decoded, err := DecodeKey[mkConfig](t.Context(), `by_level["3"].hostname`, "h")
	require.NoError(t, err)
	assert.Equal(t, fromFile.ByLevel, decoded.ByLevel)
	decoded, err = DecodeKey[mkConfig](t.Context(), `by_int["010"].host`, "o")
	require.NoError(t, err)
	assert.Equal(t, fromFile.ByInt, decoded.ByInt)

	_, err = DecodeKey[mkConfig](t.Context(), `by_int["080"]`, map[string]any{})
	assert.ErrorIs(t, err, ErrKey, "the decoder reads no integer from 080")
}

// Every source names an entry by its key's text before the sources merge,
// so precedence holds whichever spelling each source wrote, the origin and
// shadowing of a key are recorded under that text, and a source spelling one
// entry two ways fails as a key spelled two ways.
func TestMapKeys_SourcesNameAnEntryOneWay(t *testing.T) {
	file := filepath.Join(t.TempDir(), "c.yaml")
	writeFile(t, file, "by_int: {\"010\": {host: f, port: 1}}\n")
	override := Source{Kind: SourceOverride, Name: "0"}
	for range 20 {
		snap, err := LoadSnapshot[mkConfig](t.Context(), WithFile(file), WithOverrideEntries(readEntries(map[string]any{`by_int["8"].host`: "o"})))
		require.NoError(t, err)
		assert.Equal(t, mkServer{Host: "o", Port: 1}, snap.Config.ByInt[8])
		assert.Equal(t, override, snap.Provenance[`by_int["8"].host`].Input)
		assert.Equal(t, fileInput(file), snap.Provenance[`by_int["8"].port`].Input)
		assert.Equal(t, []Shadow{{Key: `by_int["8"].host`, Source: fileInput(file), By: override}}, snap.Shadowed)
	}

	writeFile(t, file, "by_int: {\"010\": {host: a}, \"8\": {host: b}}\n")
	_, err := Load[mkConfig](t.Context(), WithFile(file))
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), `keys spelled two ways set one key: by_int["8"] (as "010" and "8")`)
	_, err = DecodeEdits[mkConfig](t.Context(), nil, map[string]any{"by_int": map[string]any{"010": map[string]any{}, "8": map[string]any{}}})
	var ee *EditsError
	if assert.ErrorAs(t, err, &ee) && assert.Len(t, ee.Refused, 1) {
		assert.Contains(t, ee.Refused[0].Error(), `keys spelled two ways set one key: by_int["8"] (as "010" and "8")`)
	}
}

// mkTensKey writes itself rounded down to tens, so the text of 15 reads
// back as 10.
type mkTensKey int

func (k mkTensKey) MarshalText() ([]byte, error) { return []byte(fmt.Sprint(int(k) / 10 * 10)), nil }

func (k *mkTensKey) UnmarshalText(b []byte) error {
	_, err := fmt.Sscan(string(b), (*int)(k))
	return err
}

type mkReadBackConfig struct {
	ByFloat map[float64]string       `koanf:"by_float"`
	ByDur   map[time.Duration]string `koanf:"by_dur"`
	BySlog  map[slog.Level]string    `koanf:"by_slog"`
	ByBool  map[bool]string          `koanf:"by_bool"`
	ByTens  map[mkTensKey]string     `koanf:"by_tens"`
}

// Text names an entry only when its key is one a map holds and finds again:
// every spelling of a key is one row under the key's own text, and text
// reading as NaN, which no lookup finds, or as a key whose text reads back
// to another key, names nothing.
func TestMapKeys_TextNamesOnlyKeysThatReadBack(t *testing.T) {
	for want, spellings := range map[string][]string{
		`by_float["1.5"]`:  {`by_float["1.50"]`, `by_float["15e-1"]`, `by_float["1.5"]`},
		`by_float["+Inf"]`: {`by_float["inf"]`, `by_float["+Inf"]`},
		`by_dur["1m0s"]`:   {`by_dur["60s"]`, `by_dur["1m"]`, `by_dur["1m0s"]`},
		`by_slog["INFO"]`:  {`by_slog["info"]`, `by_slog["INFO"]`},
		`by_bool["true"]`:  {`by_bool["1"]`, `by_bool["t"]`, `by_bool["TRUE"]`, `by_bool["true"]`},
		`by_tens["20"]`:    {`by_tens["20"]`},
	} {
		for _, key := range spellings {
			got, err := CanonicalKey[mkReadBackConfig](key)
			if assert.NoError(t, err, key) {
				assert.Equal(t, want, got, key)
			}
		}
		edits := map[string]any{}
		for _, key := range spellings {
			edits[key] = "v"
		}
		got, err := DecodeEdits[mkReadBackConfig](t.Context(), nil, edits)
		if len(spellings) == 1 {
			assert.NoError(t, err, want)
			continue
		}
		var ee *EditsError
		if assert.ErrorAs(t, err, &ee, want) && assert.Len(t, ee.Refused, 1, want) {
			assert.Contains(t, ee.Refused[0].Error(), "the key is set more than once, under different spellings")
		}
		assert.Nil(t, got)
	}

	for _, key := range []string{`by_float["NaN"]`, `by_float["nan"]`, `by_tens["15"]`} {
		_, err := CanonicalKey[mkReadBackConfig](key)
		assert.ErrorIs(t, err, ErrKey, key)
		_, err = DecodeKey[mkReadBackConfig](t.Context(), key, "v")
		assert.ErrorIs(t, err, ErrKey, key)
		_, err = DecodeEdits[mkReadBackConfig](t.Context(), nil, map[string]any{key: "v"})
		assert.ErrorIs(t, err, ErrKey, key)
	}
}

// An unknown key inside an entry of a map keyed by any type is named at its
// own place, in a key that parses back to itself: the entry by its key's
// text, however the source spelled it.
func TestMapKeys_UnknownKeysAreNamedInsideEntries(t *testing.T) {
	file := filepath.Join(t.TempDir(), "c.yaml")
	writeFile(t, file, "by_int: {\"0x50\": {bogus: 1}}\nby_level: {\"03\": {bogus: 1}}\nby_text: {lvl-01: {bogus: 1}}\n")
	_, err := Load[mkConfig](t.Context(), WithFile(file))
	var u *UnknownKeysError
	require.ErrorAs(t, err, &u)
	var keys []string
	for _, k := range u.Keys {
		keys = append(keys, k.Key)
		parsed, err := ParseKey(k.Key)
		if assert.NoError(t, err, k.Key) {
			assert.Equal(t, k.Key, parsed.String())
		}
	}
	assert.ElementsMatch(t, []string{`by_int["80"].bogus`, `by_level["3"].bogus`, `by_text["lvl-1"].bogus`}, keys)

	writeFile(t, file, "by_level: {\"03\": {port: x}}\n")
	_, err = Load[mkConfig](t.Context(), WithFile(file))
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), `by_level["3"].port`)
}

// mkHookLevel is read from a name only by the caller's decode hook.
type mkHookLevel int

func readHookLevel(from, to reflect.Type, data any) (any, error) {
	name, ok := data.(string)
	if !ok || to != reflect.TypeFor[mkHookLevel]() {
		return data, nil
	}
	switch name {
	case "low":
		return mkHookLevel(1), nil
	case "high":
		return mkHookLevel(2), nil
	}
	return data, nil
}

// A load whose decode hook reads a map's keys from text the built-in
// decoding does not names what fails inside an entry by the entry's key as
// the source wrote it.
func TestMapKeys_AnEntryOnlyAHookReadsIsNamedAsWritten(t *testing.T) {
	type cfg struct {
		Levels map[mkHookLevel]mkServer `koanf:"levels"`
	}
	_, err := CanonicalKey[cfg](`levels["low"]`)
	require.ErrorIs(t, err, ErrKey, "CanonicalKey reads keys without the hook")

	file := filepath.Join(t.TempDir(), "c.yaml")
	writeFile(t, file, "levels: {low: {host: a, bogus: 1}, 2: {host: b, other: 1}}\n")
	_, err = Load[cfg](t.Context(), WithFile(file), WithDecodeHook(readHookLevel))
	var u *UnknownKeysError
	require.ErrorAs(t, err, &u)
	var keys []string
	for _, k := range u.Keys {
		keys = append(keys, k.Key)
	}
	assert.ElementsMatch(t, []string{`levels["low"].bogus`, `levels["2"].other`}, keys)

	writeFile(t, file, "levels: {low: {port: x}}\n")
	_, err = Load[cfg](t.Context(), WithFile(file), WithDecodeHook(readHookLevel))
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), `levels["low"].port`)

	writeFile(t, file, "levels: {medium: {host: a}}\n")
	_, err = Load[cfg](t.Context(), WithFile(file), WithDecodeHook(readHookLevel))
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), `levels["medium"]`)

	writeFile(t, file, "levels: {low: {host: a}}\n")
	got, err := Load[cfg](t.Context(), WithFile(file), WithDecodeHook(readHookLevel))
	require.NoError(t, err)
	assert.Equal(t, "a", got.Levels[1].Host)
}

// A key beneath an entry of a typed map an earlier key of the batch set
// edits that map's entry, read by its key's text.
func TestMapKeys_AnEditBeneathATypedMapItsBatchSets(t *testing.T) {
	got, err := DecodeEdits[mkConfig](t.Context(), nil, map[string]any{
		"by_level":            map[mkLevel]mkServer{3: {Host: "a", Hook: "h"}},
		`by_level["03"].port`: 9,
		`by_slog["WARN"]`:     "w",
	})
	require.NoError(t, err)
	assert.Equal(t, map[mkLevel]mkServer{3: {Host: "a", Port: 9, Hook: "h"}}, got.ByLevel)
	assert.Equal(t, map[slog.Level]string{slog.LevelWarn: "w"}, got.BySlog)
}

// Diff, the Applier and the printed config name and follow an entry of a
// map keyed by any type with text by that text.
func TestMapKeys_ChangesAndPrintsNameEntriesByKeyText(t *testing.T) {
	before := &mkConfig{ByInt: map[int]mkServer{80: {Host: "a"}}, ByDur: map[time.Duration]string{time.Minute: "x"}, ByText: map[mkTextLevel]mkServer{2: {}}}
	after := &mkConfig{ByInt: map[int]mkServer{80: {Host: "b"}}, ByDur: map[time.Duration]string{time.Minute: "y"}, ByText: map[mkTextLevel]mkServer{2: {Port: 7}}}
	changes, err := Diff(before, after)
	require.NoError(t, err)
	var keys []string
	for _, c := range changes {
		keys = append(keys, c.Key)
	}
	assert.ElementsMatch(t, []string{`by_dur["1m0s"]`, `by_int["80"].host`, `by_text["lvl-2"].port`}, keys)

	cfg := reflect.ValueOf(before).Elem()
	require.NoError(t, writeHeld(cfg, keyPath{}.field("by_text").key("lvl-02").field("port"), heldValue{present: true, v: reflect.ValueOf(7)}))
	assert.Equal(t, mkServer{Port: 7}, before.ByText[2])

	printed, err := RedactedMapChecked(after)
	require.NoError(t, err)
	assert.Equal(t, "b", printed[`by_int["80"].host`])
	assert.Equal(t, "y", printed[`by_dur["1m0s"]`])
}

type mkValidated struct {
	Host string `koanf:"host" validate:"required"`
}

type mkValidatedConfig struct {
	Named map[mkNamedLevel]mkValidated `koanf:"named" validate:"dive"`
	Text  map[mkTextLevel]mkValidated  `koanf:"text" validate:"dive"`
	Plain map[mkLevel]mkValidated      `koanf:"plain" validate:"dive"`
	Port  int                          `koanf:"port" default:"1" validate:"min=1"`
}

// A validation failure inside a map entry names the entry by its key's
// text — what a key writes and the decoder reads — never by how the key
// prints, and a WithInvalidEntries entry setting it is the one withheld.
func TestMapKeys_ValidationNamesEntriesByKeyText(t *testing.T) {
	file := filepath.Join(t.TempDir(), "c.yaml")
	writeFile(t, file, "named: {1: {host: \"\"}}\ntext: {lvl-2: {host: \"\"}}\nplain: {3: {host: \"\"}}\n")
	_, err := Load[mkValidatedConfig](t.Context(), WithFile(file))
	require.ErrorIs(t, err, ErrValidation)
	for _, want := range []string{`named["1"].host(required)`, `text["lvl-2"].host(required)`, `plain["3"].host(required)`} {
		assert.Contains(t, err.Error(), want)
	}
	assert.NotContains(t, err.Error(), "info")

	// A file failing beneath the entries leaves no set of them that loads,
	// so the report names what each failure is attributed to.
	writeFile(t, file, "port: 0\n")
	_, withheld, err := judged[mkValidatedConfig](t, map[string]any{`named["1"]`: map[string]any{"host": ""}}, []Option{WithFile(file)})
	require.ErrorIs(t, err, ErrValidation)
	assert.Equal(t, []string{`named["1"]`}, withheld)
}
