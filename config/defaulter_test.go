package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The issue's examples: a shared section each program sets differently,
// and one type at several paths with different defaults.

type queueSection struct {
	Channel string        `koanf:"channel" default:"default"`
	Topic   string        `koanf:"topic"`
	Timeout time.Duration `koanf:"timeout" default:"30s"`
}

// SetDefaults is the section's own: overridden by an enclosing struct's.
func (q *queueSection) SetDefaults() { q.Topic = "events" }

type broker struct {
	Host string `koanf:"host"`
	User string `koanf:"user" default:"guest"`
}

type brokers struct {
	Primary   broker `koanf:"primary"`
	Secondary broker `koanf:"secondary"`
}

func (b *brokers) SetDefaults() { b.Primary.Host = "tcp://localhost:1883" }

type codeRoute struct {
	Timeout time.Duration `koanf:"timeout" default:"5s"`
	Retries int           `koanf:"retries"`
	Mode    string        `koanf:"mode" default:"tag"`
}

func (r *codeRoute) SetDefaults() { r.Retries, r.Mode = 3, "code" }

type tlsSection struct {
	MinVersion string `koanf:"min_version"`
}

func (t *tlsSection) SetDefaults() { t.MinVersion = "1.2" }

type unusedSection struct {
	Note string `koanf:"note"`
}

func (*unusedSection) SetDefaults() {}

type workerConfig struct {
	Queue    queueSection         `koanf:"queue"`
	Brokers  brokers              `koanf:"brokers"`
	Routes   map[string]codeRoute `koanf:"routes"`
	Steps    []codeRoute          `koanf:"steps"`
	TLS      *tlsSection          `koanf:"tls"`
	Unused   *unusedSection       `koanf:"unused"`
	Listen   netip.AddrPort       `koanf:"listen"`
	Password string               `koanf:"password"`
	Labels   map[string]string    `koanf:"labels" default:"team=core"`
}

// The worker's own channel; every other queue field keeps its tag or the
// section's SetDefaults.
func (w *workerConfig) SetDefaults() {
	w.Queue.Channel = "worker"
	w.Listen = netip.MustParseAddrPort("127.0.0.1:9000")
	w.Password = "FAKE_CODE_SECRET"
	w.Labels["tier"] = "batch"
}

func TestSetDefaults_LayerAboveTags(t *testing.T) {
	cfg, err := Load[workerConfig](t.Context())
	require.NoError(t, err)
	assert.Equal(t, queueSection{Channel: "worker", Topic: "events", Timeout: 30 * time.Second}, cfg.Queue,
		"the enclosing struct's SetDefaults overrides the section's; the tag stays where neither sets")
	assert.Equal(t, broker{Host: "tcp://localhost:1883", User: "guest"}, cfg.Brokers.Primary, "a field SetDefaults leaves keeps its tag")
	assert.Equal(t, broker{User: "guest"}, cfg.Brokers.Secondary)
	require.NotNil(t, cfg.TLS, "a pointer section whose SetDefaults sets something is allocated")
	assert.Equal(t, "1.2", cfg.TLS.MinVersion)
	assert.Nil(t, cfg.Unused, "one whose SetDefaults sets nothing stays nil")
	assert.Equal(t, netip.MustParseAddrPort("127.0.0.1:9000"), cfg.Listen, "a value that keeps state in unexported fields")
	assert.Equal(t, map[string]string{"team": "core", "tier": "batch"}, cfg.Labels)
}

// Every source outranks the code defaults, which outrank the tags.
func TestSetDefaults_Precedence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "queue:\n  topic: from-file\nlabels:\n  owner: ops\ntls: null\n")
	cfg, err := Load[workerConfig](t.Context(),
		WithDefaults(map[string]any{"queue.channel": "programmatic"}),
		WithFile(file),
		WithEnvPrefix("APP"), WithEnviron(func() []string { return []string{"APP_BROKERS_PRIMARY_HOST=tcp://env:1883"} }),
	)
	require.NoError(t, err)
	assert.Equal(t, "programmatic", cfg.Queue.Channel, "WithDefaults is above SetDefaults")
	assert.Equal(t, "from-file", cfg.Queue.Topic)
	assert.Equal(t, "tcp://env:1883", cfg.Brokers.Primary.Host)
	assert.Nil(t, cfg.TLS, "a source's null turns the section off")
	assert.Equal(t, map[string]string{"team": "core", "tier": "batch", "owner": "ops"}, cfg.Labels, "a map merges entry by entry")
}

// An element type's SetDefaults runs for each entry a source creates, after
// its tags; what the source set stays.
func TestSetDefaults_Elements(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "routes:\n  a: {timeout: 1s}\n  b: {}\n  c: {retries: 0}\nsteps:\n  - {}\n  - {retries: 7}\n")
	snap, err := LoadSnapshot[workerConfig](t.Context(), WithFile(file))
	require.NoError(t, err)
	cfg := snap.Config
	assert.Equal(t, map[string]codeRoute{
		"a": {Timeout: time.Second, Retries: 3, Mode: "code"},
		"b": {Timeout: 5 * time.Second, Retries: 3, Mode: "code"},
		"c": {Timeout: 5 * time.Second, Retries: 0, Mode: "code"},
	}, cfg.Routes)
	assert.Equal(t, []codeRoute{{Timeout: 5 * time.Second, Retries: 3, Mode: "code"}, {Timeout: 5 * time.Second, Retries: 7, Mode: "code"}}, cfg.Steps)

	kind := func(key string) SourceKind { return snap.Provenance[key].Input.Kind }
	assert.Equal(t, SourceCodeDefaults, kind(`routes["b"].retries`))
	assert.Equal(t, SourceTag, kind(`routes["b"].timeout`))
	assert.Equal(t, SourceCodeDefaults, kind(`routes["b"].mode`), "an element's SetDefaults outranks its tag")
	assert.Equal(t, SourceFile, kind(`routes["c"].retries`))
	assert.Equal(t, SourceCodeDefaults, kind("queue.channel"))
	assert.Equal(t, SourceCodeDefaults, kind("queue.topic"))
	assert.Equal(t, SourceTag, kind("queue.timeout"))
	assert.Equal(t, SourceTag, kind("brokers.secondary.user"))
}

// Usage, the JSON Schema and the starter file show the effective defaults;
// one SetDefaults sets on a sensitive field stays withheld from all of them.
// The starter file loads as no source at all.
func TestSetDefaults_Metadata(t *testing.T) {
	rows, err := Usage[workerConfig]("APP")
	require.NoError(t, err)
	byKey := map[string]UsageEntry{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	assert.Equal(t, "worker", byKey["queue.channel"].Default)
	assert.Equal(t, "events", byKey["queue.topic"].Default)
	assert.Equal(t, "30s", byKey["queue.timeout"].Default, "a tag-only default is shown as written")
	assert.Equal(t, "tcp://localhost:1883", byKey["brokers.primary.host"].Default)
	assert.Empty(t, byKey["brokers.secondary.host"].Default)
	assert.Equal(t, "3", byKey["routes[*].retries"].Default, "an element type's SetDefaults")
	assert.Equal(t, "5s", byKey["routes[*].timeout"].Default)
	assert.Equal(t, "code", byKey["routes[*].mode"].Default)
	assert.True(t, byKey["password"].Withheld)
	assert.Empty(t, byKey["password"].Default)

	schema := jsonSchemaOf[workerConfig](t)
	assert.Equal(t, "worker", prop(t, prop(t, schema, "queue"), "channel")["default"])
	assert.NotContains(t, prop(t, schema, "password"), "default")

	var buf bytes.Buffer
	require.NoError(t, PrintTemplate[workerConfig]("APP", WithFormat(FormatJSON), WithWriter(&buf)))
	assert.NotContains(t, buf.String(), "FAKE_CODE_SECRET")
	var tree map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &tree))
	fromTemplate, err := Decode[workerConfig](t.Context(), tree)
	require.NoError(t, err)
	want, err := Load[workerConfig](t.Context())
	require.NoError(t, err)
	assert.Equal(t, want, fromTemplate)

	var yaml bytes.Buffer
	require.NoError(t, PrintTemplate[workerConfig]("APP", WithWriter(&yaml)))
	assert.Contains(t, yaml.String(), "channel: worker")
	assert.NotContains(t, yaml.String(), "FAKE_CODE_SECRET")
}

// CheckKey and DecodeKey judge a stored entry against the same defaults.
func TestSetDefaults_KeyChecks(t *testing.T) {
	cfg, err := DecodeKey[workerConfig](t.Context(), "queue.topic", "jobs")
	require.NoError(t, err)
	assert.Equal(t, "worker", cfg.Queue.Channel)
	assert.Equal(t, "jobs", cfg.Queue.Topic)
	assert.NoError(t, CheckKey[workerConfig](t.Context(), `routes["x"].timeout`, "2s"))
	cfg, err = DecodeKey[workerConfig](t.Context(), `routes["x"].timeout`, "2s")
	require.NoError(t, err)
	assert.Equal(t, codeRoute{Timeout: 2 * time.Second, Retries: 3, Mode: "code"}, cfg.Routes["x"])
}

// A reload keeps the code defaults; they are beneath every source, so a
// file that sets one shadows nothing.
func TestSetDefaults_Reload(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "queue:\n  channel: from-file\n")
	r, err := NewReloader[workerConfig](t.Context(), []Option{WithFile(file)})
	require.NoError(t, err)
	assert.Empty(t, r.Snapshot().Shadowed)
	writeFile(t, file, "queue:\n  topic: edited\n")
	res, err := r.Reload(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "worker", r.Config().Queue.Channel, "the file's key gone, the code default returns")
	assert.Equal(t, "edited", r.Config().Queue.Topic)
	assert.Empty(t, res.Shadowed)
	assert.Equal(t, []string{"queue.channel", "queue.topic"}, changedKeys(res.Changes))
}

// A tag that only a load's decode hook can decode still reaches
// SetDefaults: the code defaults decode the tags with the load's hooks.
type hookedLevel int

type hookedConfig struct {
	Level hookedLevel `koanf:"level" default:"loud"`
	Name  string      `koanf:"name"`
}

func (h *hookedConfig) SetDefaults() { h.Name = "hooked" }

func TestSetDefaults_DecodesTagsWithTheLoadsHooks(t *testing.T) {
	hook := mapstructure.DecodeHookFuncType(func(_, to reflect.Type, data any) (any, error) {
		if to == reflect.TypeFor[hookedLevel]() && data == "loud" {
			return hookedLevel(10), nil
		}
		return data, nil
	})
	cfg, err := Load[hookedConfig](t.Context(), WithDecodeHook(hook))
	require.NoError(t, err)
	assert.Equal(t, hookedConfig{Level: 10, Name: "hooked"}, *cfg)
}

// Embedded structs promote their SetDefaults as Go promotes methods.

type commonSection struct {
	Region string `koanf:"region"`
	Zone   string `koanf:"zone"`
}

func (c *commonSection) SetDefaults() { c.Region, c.Zone = "us-east-1", "a" }

type promotes struct {
	commonSection `koanf:",squash"`
	Name          string `koanf:"name"`
}

type overrides struct {
	commonSection `koanf:",squash"`
}

// SetDefaults shadows the promoted one, so it calls it, as Go requires.
func (o *overrides) SetDefaults() {
	o.commonSection.SetDefaults()
	o.Zone = "b"
}

type otherDefaulter struct {
	Extra string `koanf:"extra"`
}

func (o *otherDefaulter) SetDefaults() { o.Extra = "x" }

type collides struct {
	commonSection  `koanf:",squash"`
	otherDefaulter `koanf:",squash"`
}

type valueReceiver struct {
	Port int `koanf:"port"`
}

//lint:ignore SA4005 a value receiver is the mistake under test
func (v valueReceiver) SetDefaults() { v.Port = 80 }

type holdsValueReceiver struct {
	Inner valueReceiver `koanf:"inner"`
}

func TestSetDefaults_Embedding(t *testing.T) {
	p, err := Load[promotes](t.Context())
	require.NoError(t, err)
	assert.Equal(t, commonSection{Region: "us-east-1", Zone: "a"}, p.commonSection, "a promoted SetDefaults runs once")
	o, err := Load[overrides](t.Context())
	require.NoError(t, err)
	assert.Equal(t, commonSection{Region: "us-east-1", Zone: "b"}, o.commonSection)

	_, err = Load[collides](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "collide")
	for _, load := range []func() error{
		func() error { _, err := Load[valueReceiver](t.Context()); return err },
		func() error { _, err := Load[holdsValueReceiver](t.Context()); return err },
		func() error { _, err := Usage[holdsValueReceiver](); return err },
		func() error { return PrintTemplate[holdsValueReceiver]("", WithWriter(&bytes.Buffer{})) },
	} {
		err := load()
		require.ErrorIs(t, err, ErrSchema)
		assert.Contains(t, err.Error(), "value receiver")
	}
}

// SetDefaults can turn a tagged section off and set a whole section or a
// list; the elements of a list it sets take their element defaults.
type defaultsBlock struct {
	Count int    `koanf:"count" default:"2"`
	Label string `koanf:"label"`
}

type defaultsBranches struct {
	Off     *defaultsBlock  `koanf:"off"`
	On      defaultsBlock   `koanf:"on"`
	Entries []defaultsBlock `koanf:"entries"`
}

func (d *defaultsBranches) SetDefaults() {
	d.Off = nil
	d.On = defaultsBlock{Count: 9, Label: "declared"}
	d.Entries = []defaultsBlock{{Label: "first"}}
}

func TestSetDefaults_NullStructsAndLists(t *testing.T) {
	want, err := Load[defaultsBranches](t.Context())
	require.NoError(t, err)
	assert.Nil(t, want.Off)
	assert.Equal(t, defaultsBlock{Count: 9, Label: "declared"}, want.On)
	assert.Equal(t, []defaultsBlock{{Count: 2, Label: "first"}}, want.Entries)

	rows, err := Usage[defaultsBranches]("")
	require.NoError(t, err)
	byKey := map[string]UsageEntry{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	assert.False(t, byKey["off.count"].hasDefault, "the section SetDefaults turned off has no defaults")
	assert.Equal(t, "declared", byKey["on.label"].Default)
	assert.JSONEq(t, `[{"count":2,"label":"first"}]`, byKey["entries"].Default)

	var buf bytes.Buffer
	require.NoError(t, PrintTemplate[defaultsBranches]("", WithWriter(&buf), WithFormat(FormatJSON)))
	var tree map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &tree))
	got, err := Decode[defaultsBranches](t.Context(), tree)
	require.NoError(t, err)
	assert.Equal(t, want, got)
	schema := jsonSchemaOf[defaultsBranches](t)
	assert.Contains(t, prop(t, schema, "off"), "default")
	assert.Equal(t, "declared", prop(t, prop(t, schema, "on"), "label")["default"])

	got, err = Load[defaultsBranches](t.Context(),
		WithBaseProvider(confmap.Provider(map[string]any{"on.count": 1}, "."), nil))
	require.NoError(t, err)
	assert.Equal(t, defaultsBlock{Count: 1, Label: "declared"}, got.On)
}

// A named non-struct type can set its own default, at every path it
// appears.
type portNumber int

func (p *portNumber) SetDefaults() { *p = 8080 }

type withPorts struct {
	HTTP  portNumber  `koanf:"http"`
	Admin *portNumber `koanf:"admin"`
}

func TestSetDefaults_NamedFieldTypes(t *testing.T) {
	cfg, err := Load[withPorts](t.Context())
	require.NoError(t, err)
	assert.Equal(t, portNumber(8080), cfg.HTTP)
	require.NotNil(t, cfg.Admin)
	assert.Equal(t, portNumber(8080), *cfg.Admin)
}

// A section allocated for its own SetDefaults stays allocated until every
// SetDefaults has run, so an enclosing one can set it.
type certSection struct {
	Cert string `koanf:"cert"`
}

func (*certSection) SetDefaults() {}

type listenerSection struct {
	TLS *certSection `koanf:"tls"`
}

type serverConfig struct {
	Listener listenerSection `koanf:"listener"`
}

func (s *serverConfig) SetDefaults() { s.Listener.TLS.Cert = "/etc/server.pem" }

func TestSetDefaults_EnclosingSetsAnAllocatedSection(t *testing.T) {
	cfg, err := Load[serverConfig](t.Context())
	require.NoError(t, err)
	require.NotNil(t, cfg.Listener.TLS)
	assert.Equal(t, "/etc/server.pem", cfg.Listener.TLS.Cert)
}

// Structs that marshal to text keep their state through the code defaults
// layer wherever SetDefaults puts them: a list, an array, a map — an entry
// changed or one removed — or a section inside a map — and a nil list stays
// nil. A time.Time, which the merge copies whole, keeps its location's name
// too.
type addrSection struct {
	Gateway netip.Addr `koanf:"gateway"`
}

type textDefaults struct {
	Prefix   netip.Prefix            `koanf:"prefix"`
	Prefixes []netip.Prefix          `koanf:"prefixes"`
	Addrs    []netip.Addr            `koanf:"addrs"`
	Pair     [2]netip.Addr           `koanf:"pair"`
	Pointers []*netip.Addr           `koanf:"pointers"`
	Changed  map[string]netip.Prefix `koanf:"changed" default:"a=10.0.0.0/8,b=10.1.0.0/16"`
	Removed  map[string]netip.Prefix `koanf:"removed" default:"a=10.0.0.0/8,b=10.1.0.0/16"`
	ByIndex  map[int]netip.Addr      `koanf:"by_index"`
	Lists    map[string][]netip.Addr `koanf:"lists"`
	Sections map[string]addrSection  `koanf:"sections"`
	Hosts    map[int]addrSection     `koanf:"hosts"`
	Unset    map[int][]netip.Addr    `koanf:"unset"`
	Start    time.Time               `koanf:"start"`
	Windows  []time.Time             `koanf:"windows"`
}

// testZone is a named zone a text round trip would lose the name of.
var testZone = time.FixedZone("TEST", 3600)

func (d *textDefaults) SetDefaults() {
	d.Prefix = netip.MustParsePrefix("10.0.0.0/8")
	d.Prefixes = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.168.0.0/16")}
	d.Addrs = []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("2001:db8::1")}
	d.Pair = [2]netip.Addr{netip.MustParseAddr("10.0.0.3"), netip.MustParseAddr("10.0.0.4")}
	addr := netip.MustParseAddr("10.0.0.5")
	d.Pointers = []*netip.Addr{&addr, nil}
	d.Changed["b"] = netip.MustParsePrefix("10.2.0.0/16")
	delete(d.Removed, "b")
	d.ByIndex = map[int]netip.Addr{1: netip.MustParseAddr("10.0.0.6")}
	d.Lists = map[string][]netip.Addr{"x": {netip.MustParseAddr("10.0.0.7")}}
	d.Sections = map[string]addrSection{"lan": {Gateway: netip.MustParseAddr("192.168.0.1")}}
	d.Hosts = map[int]addrSection{1: {Gateway: netip.MustParseAddr("192.168.1.1")}}
	d.Unset = map[int][]netip.Addr{1: nil}
	d.Start = time.Date(2026, 1, 2, 3, 4, 5, 0, testZone)
	d.Windows = []time.Time{time.Date(2026, 2, 3, 4, 5, 6, 0, testZone)}
}

func TestSetDefaults_ValuesThatMarshalToText(t *testing.T) {
	want := textDefaults{
		Changed: map[string]netip.Prefix{"a": netip.MustParsePrefix("10.0.0.0/8"), "b": netip.MustParsePrefix("10.1.0.0/16")},
		Removed: map[string]netip.Prefix{"a": netip.MustParsePrefix("10.0.0.0/8"), "b": netip.MustParsePrefix("10.1.0.0/16")},
	}
	want.SetDefaults()

	got, err := Load[textDefaults](t.Context())
	require.NoError(t, err)
	assert.Equal(t, want, *got)

	rows, err := Usage[textDefaults]("")
	require.NoError(t, err)
	for _, r := range rows {
		if r.Key == "prefixes" {
			assert.Equal(t, `["10.0.0.0/8","192.168.0.0/16"]`, r.Default)
		}
	}
}

// failingText marshals to text only when n is not negative.
type failingText struct{ n int }

func (f failingText) MarshalText() ([]byte, error) {
	if f.n < 0 {
		return nil, errors.New("negative")
	}
	return []byte(strconv.Itoa(f.n)), nil
}

func (f *failingText) UnmarshalText(b []byte) error {
	n, err := strconv.Atoi(string(b))
	f.n = n
	return err
}

type failingDefaults struct {
	Values []failingText `koanf:"values"`
}

func (d *failingDefaults) SetDefaults() { d.Values = []failingText{{n: 1}, {n: -1}} }

// A value SetDefaults sets that cannot marshal to text fails the load, naming
// its key, instead of loading as a zero value.
func TestSetDefaults_ValueThatCannotMarshalFails(t *testing.T) {
	_, err := Load[failingDefaults](t.Context())
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "values[1]")
	assert.NotContains(t, err.Error(), "values[0]")
}

type dynamicDefaults struct {
	Any any `koanf:"any"`
}

func (d *dynamicDefaults) SetDefaults() { d.Any = netip.MustParseAddr("10.0.0.9") }

// A field of interface type that SetDefaults sets to a struct whose state
// the merge would lose receives the struct's text.
func TestSetDefaults_InterfaceFieldTakesText(t *testing.T) {
	got, err := Load[dynamicDefaults](t.Context())
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.9", got.Any)
}

// selfDecodingMap decodes itself, so config carries it whole: it has no
// entries of its own to merge, and a typed value set in code reaches its
// decoder as that value.
type selfDecodingMap map[string]string

func (m *selfDecodingMap) UnmarshalMapstructure(v any) error {
	switch in := v.(type) {
	case selfDecodingMap:
		*m = maps.Clone(in)
	case map[string]any:
		*m = selfDecodingMap{}
		for k, e := range in {
			(*m)[k] = fmt.Sprint(e)
		}
	default:
		return fmt.Errorf("decoding %T", v)
	}
	return nil
}

type selfDecodingMapCfg struct {
	Labels selfDecodingMap `koanf:"labels"`
	Name   string          `koanf:"name"`
}

func (c *selfDecodingMapCfg) SetDefaults() { c.Labels = selfDecodingMap{"a": "1"} }

// A Defaulter may set a map that decodes itself: it is one value, set
// whole, and a higher source that sets the map replaces it whole.
func TestSetDefaults_SelfDecodingMapIsOneValue(t *testing.T) {
	var got *selfDecodingMapCfg
	var err error
	require.NotPanics(t, func() { got, err = Load[selfDecodingMapCfg](t.Context()) })
	require.NoError(t, err)
	assert.Equal(t, selfDecodingMap{"a": "1"}, got.Labels)

	above, err := NewMapProvider(func(context.Context) (map[string]any, error) {
		entries := map[string]any{"labels": map[string]any{"b": "2"}}
		return entries, nil
	}, "")
	require.NoError(t, err)
	got, err = Load[selfDecodingMapCfg](t.Context(), WithBaseProvider(above, nil))
	require.NoError(t, err)
	assert.Equal(t, selfDecodingMap{"b": "2"}, got.Labels)
}
