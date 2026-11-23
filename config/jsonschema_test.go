package config

import (
	"encoding/json"
	"maps"
	"net/url"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type schemaTree struct {
	Name     string        `koanf:"name"`
	Children []*schemaTree `koanf:"children"`
}

type schemaLimits struct {
	Rate  int    `koanf:"rate" default:"10" validate:"min=1,max=100" desc:"Requests per second"`
	Label string `koanf:"label" default:"std"`
}

type SchemaBase struct {
	Region string `koanf:"region" default:"eu" validate:"oneof=eu us"`
}

type schemaExtra struct {
	Keep  string         `koanf:"keep"`
	Other map[string]any `koanf:",remain"`
}

type schemaCfg struct {
	SchemaBase `koanf:",squash"`

	Port    int                     `koanf:"port" default:"8080" validate:"required,min=1,max=65535" desc:"HTTP port"`
	Timeout time.Duration           `koanf:"timeout" default:"30s"`
	Debug   bool                    `koanf:"debug" default:"true"`
	Ratio   float64                 `koanf:"ratio" validate:"gt=0,lte=1"`
	Mode    string                  `koanf:"mode" validate:"omitempty,min=3"`
	Level   int                     `koanf:"level" validate:"oneof=1 2 3"`
	Tags    []string                `koanf:"tags" validate:"min=1,max=5"`
	Pair    [2]int                  `koanf:"pair"`
	Limits  map[string]schemaLimits `koanf:"limits" validate:"max=10"`
	Opt     *schemaLimits           `koanf:"opt"`
	Nested  *struct {
		Inner *schemaLimits `koanf:"inner"`
	} `koanf:"nested"`
	Started  time.Time         `koanf:"started"`
	Plugin   any               `koanf:"plugin"`
	Tree     schemaTree        `koanf:"tree"`
	Extra    schemaExtra       `koanf:"extra"`
	Password string            `koanf:"password" default:"hunter2" validate:"min=8,oneof=hunter2 secret12"`
	Token    string            `koanf:"token" sensitive:"true" validate:"eq=abcdef"`
	Secrets  map[string]string `koanf:"secrets"`
}

func jsonSchemaOf[T any](t *testing.T) map[string]any {
	t.Helper()
	raw, err := JSONSchema[T]()
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	return doc
}

func prop(t *testing.T, s map[string]any, key string) map[string]any {
	t.Helper()
	props, ok := s["properties"].(map[string]any)
	require.True(t, ok, "no properties in %v", s)
	p, ok := props[key].(map[string]any)
	require.True(t, ok, "no property %s", key)
	return p
}

// nonNull is the schema of a nullable key's value.
func nonNull(t *testing.T, s map[string]any) map[string]any {
	t.Helper()
	alts, ok := s["anyOf"].([]any)
	require.True(t, ok, "not nullable: %v", s)
	assert.Equal(t, map[string]any{"type": "null"}, alts[1])
	return alts[0].(map[string]any)
}

func TestJSONSchema_Structure(t *testing.T) {
	doc := jsonSchemaOf[schemaCfg](t)
	assert.Equal(t, JSONSchemaDraft, doc["$schema"])
	assert.Equal(t, "object", doc["type"])
	assert.Equal(t, false, doc["additionalProperties"], "a key no field takes fails the load")
	assert.Equal(t, "string", prop(t, doc, "region")["type"], "a squashed struct's keys sit in place")

	limits := nonNull(t, prop(t, doc, "limits"))
	assert.Equal(t, "object", limits["type"])
	elem := limits["additionalProperties"].(map[string]any)
	assert.Equal(t, map[string]any{"type": "integer", "default": 10.0, "minimum": 1.0, "maximum": 100.0,
		"description": "Requests per second", "x-config-validation": []any{"min", "max"}, "x-reload": ReloadRestart}, prop(t, elem, "rate"),
		"an element struct's defaults and rules")

	tags := nonNull(t, prop(t, doc, "tags"))
	assert.Equal(t, map[string]any{"type": "string"}, tags["items"])
	pair := prop(t, doc, "pair")
	assert.Equal(t, 2.0, pair["minItems"])
	assert.Equal(t, 2.0, pair["maxItems"])

	opt := nonNull(t, prop(t, doc, "opt"))
	assert.Equal(t, "object", opt["type"])

	tree := prop(t, doc, "tree")
	assert.Equal(t, "#/$defs/config.schemaTree", tree["$ref"], "a recursive struct is referenced")
	defs := doc["$defs"].(map[string]any)
	def := defs["config.schemaTree"].(map[string]any)
	children := nonNull(t, prop(t, def, "children"))
	assert.Equal(t, map[string]any{"anyOf": []any{map[string]any{"$ref": "#/$defs/config.schemaTree"}, map[string]any{"type": "null"}}}, children["items"])

	assert.Equal(t, true, prop(t, doc, "extra")["additionalProperties"], "a remain field takes every other key")
}

func TestJSONSchema_TypedDefaultsAndOpaqueTypes(t *testing.T) {
	doc := jsonSchemaOf[schemaCfg](t)
	assert.Equal(t, 8080.0, prop(t, doc, "port")["default"])
	assert.Equal(t, "HTTP port", prop(t, doc, "port")["description"])
	timeout := prop(t, doc, "timeout")
	assert.Equal(t, "string", timeout["type"])
	assert.Equal(t, "30s", timeout["default"], "a duration as the text it decodes from")
	assert.Equal(t, "time.Duration", timeout["x-config-type"])
	assert.Equal(t, true, prop(t, doc, "debug")["default"])
	started := prop(t, doc, "started")
	assert.Equal(t, "string", started["type"], "a type that decodes from text")
	assert.Equal(t, "time.Time", started["x-config-type"])
	plugin := nonNull(t, prop(t, doc, "plugin"))
	assert.Equal(t, "interface {}", plugin["x-config-type"])
	assert.NotContains(t, plugin, "type", "an interface is unconstrained")
}

func TestJSONSchema_Rules(t *testing.T) {
	doc := jsonSchemaOf[schemaCfg](t)
	port := prop(t, doc, "port")
	assert.Equal(t, 1.0, port["minimum"])
	assert.Equal(t, 65535.0, port["maximum"])
	assert.Equal(t, []any{"required", "min", "max"}, port["x-config-validation"])
	assert.NotContains(t, doc, "required", "a file need not set a required key: another source may")

	ratio := prop(t, doc, "ratio")
	assert.Equal(t, 0.0, ratio["exclusiveMinimum"])
	assert.Equal(t, 1.0, ratio["maximum"])
	assert.Equal(t, []any{1.0, 2.0, 3.0}, prop(t, doc, "level")["enum"])
	assert.Equal(t, []any{"eu", "us"}, prop(t, doc, "region")["enum"])

	mode := prop(t, doc, "mode")
	assert.NotContains(t, mode, "minLength", "the bound applies only to nonempty strings")
	assert.Contains(t, mode, "allOf")
	assert.Equal(t, []any{"omitempty", "min"}, mode["x-config-validation"])

	tags := nonNull(t, prop(t, doc, "tags"))
	assert.Equal(t, 1.0, tags["minItems"])
	assert.Equal(t, 5.0, tags["maxItems"])
	assert.Equal(t, 10.0, nonNull(t, prop(t, doc, "limits"))["maxProperties"])
}

// A sensitive key is writeOnly, with no default, enum or const: an operand
// can be a credential. Its size bounds stay.
func TestJSONSchema_SensitiveKeysCarryNoValues(t *testing.T) {
	raw, err := JSONSchema[schemaCfg]()
	require.NoError(t, err)
	for _, secret := range []string{"hunter2", "secret12", "abcdef"} {
		assert.NotContains(t, string(raw), secret)
	}
	var doc map[string]any
	require.NoError(t, json.Unmarshal(raw, &doc))
	password := prop(t, doc, "password")
	assert.Equal(t, map[string]any{"type": "string", "writeOnly": true, "minLength": 8.0, "x-config-validation": []any{"min", "oneof"}, "x-reload": ReloadRestart}, password)
	token := prop(t, doc, "token")
	assert.Equal(t, true, token["writeOnly"])
	assert.NotContains(t, token, "const")
	secrets := nonNull(t, prop(t, doc, "secrets"))
	assert.NotContains(t, secrets, "additionalProperties", "a sensitive map's entries are not described")
}

func TestJSONSchema_DeterministicAndRefusesNonStructs(t *testing.T) {
	a, err := JSONSchema[schemaCfg]()
	require.NoError(t, err)
	b, err := JSONSchema[schemaCfg]()
	require.NoError(t, err)
	assert.Equal(t, string(a), string(b))

	_, err = JSONSchema[int]()
	require.ErrorIs(t, err, ErrSchema)
}

type schemaGenericTree[T any] struct {
	Next  *schemaGenericTree[T] `koanf:"next"`
	Value T                     `koanf:"value"`
}

type schemaGenericCfg struct {
	Tree schemaGenericTree[url.URL] `koanf:"tree"`
}

// A definition's name is one JSON Pointer token and a URI fragment as
// written, whatever its Go type's name holds.
func TestJSONSchema_DefinitionNamesResolve(t *testing.T) {
	doc := jsonSchemaOf[schemaGenericCfg](t)
	defs, ok := doc["$defs"].(map[string]any)
	require.True(t, ok)
	require.Len(t, defs, 1)
	for name := range defs {
		assert.Regexp(t, `^[A-Za-z0-9._-]+$`, name)
	}
	ref, ok := prop(t, doc, "tree")["$ref"].(string)
	require.True(t, ok)
	assert.Contains(t, defs, strings.TrimPrefix(ref, "#/$defs/"))
}

type SchemaRemain struct {
	Other map[string]any `koanf:",remain"`
}

type schemaSquashedRemain struct {
	SchemaRemain `koanf:",squash"`
}

type schemaQuotedOneOf struct {
	Mode  string  `koanf:"mode" validate:"oneof='hello world' goodbye"`
	Count int     `koanf:"count" validate:"oneof=1 05 +7 8"`
	Ratio float64 `koanf:"ratio" validate:"oneof=0.5 1"`
}

// Rules are read as the validator reads them: quoted values keep their
// spaces, an integer matches only its decimal text, and a remain field a
// squashed struct holds opens the object.
func TestJSONSchema_RulesAsTheValidatorReadsThem(t *testing.T) {
	doc := jsonSchemaOf[schemaQuotedOneOf](t)
	assert.Equal(t, []any{"hello world", "goodbye"}, prop(t, doc, "mode")["enum"])
	assert.Equal(t, []any{1.0, 8.0}, prop(t, doc, "count")["enum"])
	assert.NotContains(t, prop(t, doc, "ratio"), "enum", "the validator does not compare floats")

	doc = jsonSchemaOf[schemaSquashedRemain](t)
	assert.Equal(t, true, doc["additionalProperties"])
}

// schemaRecNode is a recursive section whose annotations come from where it
// is held: a reload class and sensitivity it inherits.
type schemaRecNode struct {
	Value    int             `koanf:"value"`
	Label    string          `koanf:"label" example:"SYNTHETIC-EXAMPLE" validate:"omitempty,oneof=SYNTHETIC-ENUM other"`
	Children []schemaRecNode `koanf:"children"`
}

type schemaLiveFirst struct {
	Live    schemaRecNode `koanf:"live" reload:"live"`
	Restart schemaRecNode `koanf:"restart" reload:"restart"`
	Hidden  schemaRecNode `koanf:"hidden" sensitive:"true"`
	Again   schemaRecNode `koanf:"again" reload:"live"`
}

type schemaRestartFirst struct {
	Restart schemaRecNode `koanf:"restart" reload:"restart"`
	Hidden  schemaRecNode `koanf:"hidden" sensitive:"true"`
	Live    schemaRecNode `koanf:"live" reload:"live"`
	Again   schemaRecNode `koanf:"again" reload:"live"`
}

// resolved is s, or the $defs entry s refers to.
func resolved(t *testing.T, doc, s map[string]any) map[string]any {
	t.Helper()
	ref, ok := s["$ref"].(string)
	if !ok {
		return s
	}
	name, found := strings.CutPrefix(ref, "#/$defs/")
	require.True(t, found, "a local reference: %s", ref)
	def, ok := doc["$defs"].(map[string]any)[name].(map[string]any)
	require.True(t, ok, "%s resolves", ref)
	return def
}

// assertRefsResolve checks that every $ref in v names a $defs entry.
func assertRefsResolve(t *testing.T, doc map[string]any, v any) {
	t.Helper()
	switch v := v.(type) {
	case map[string]any:
		if _, ok := v["$ref"]; ok {
			resolved(t, doc, v)
		}
		for _, e := range v {
			assertRefsResolve(t, doc, e)
		}
	case []any:
		for _, e := range v {
			assertRefsResolve(t, doc, e)
		}
	}
}

// A recursive section held by keys of different reload classes or
// sensitivity has a $defs entry per policy, so following any key's
// references reaches the annotations FieldFor gives it, in either
// declaration order; keys with one policy share an entry.
func TestJSONSchema_RecursiveDefinitionsKeepInheritedPolicy(t *testing.T) {
	for name, schema := range map[string]func(*testing.T) map[string]any{
		"live first":    jsonSchemaOf[schemaLiveFirst],
		"restart first": jsonSchemaOf[schemaRestartFirst],
	} {
		t.Run(name, func(t *testing.T) {
			doc := schema(t)
			assertRefsResolve(t, doc, doc)
			assert.Len(t, doc["$defs"], 3, "live, restart and sensitive: again shares live's")
			assert.Equal(t, prop(t, doc, "live")["$ref"], prop(t, doc, "again")["$ref"])
			hidden := resolved(t, doc, prop(t, doc, "hidden"))
			assert.Equal(t, map[string]any{"type": "array"}, nonNull(t, prop(t, hidden, "children")), "a sensitive list describes nothing inside")
			for _, owner := range []string{"live", "restart", "hidden"} {
				node := resolved(t, doc, prop(t, doc, owner))
				child, grandchild := node, node
				if owner != "hidden" {
					child = resolved(t, doc, nonNull(t, prop(t, node, "children"))["items"].(map[string]any))
					grandchild = resolved(t, doc, nonNull(t, prop(t, child, "children"))["items"].(map[string]any))
				}
				for key, s := range map[string]map[string]any{
					owner + ".value":                         prop(t, node, "value"),
					owner + ".children[0].label":             prop(t, child, "label"),
					owner + ".children[0].children[1].value": prop(t, grandchild, "value"),
				} {
					var f Field
					switch name {
					case "live first":
						f = mustFieldFor[schemaLiveFirst](t, key)
					default:
						f = mustFieldFor[schemaRestartFirst](t, key)
					}
					assert.Equal(t, f.Reload, s["x-reload"], key)
					assert.Equal(t, f.Sensitive, s["writeOnly"] == true, key)
				}
				label := prop(t, child, "label")
				text, err := json.Marshal(node)
				require.NoError(t, err)
				if owner == "hidden" {
					assert.NotContains(t, string(text), "SYNTHETIC-", "a sensitive owner's entry shows no example or enum")
				} else {
					assert.Equal(t, []any{"SYNTHETIC-EXAMPLE"}, label["examples"])
					assert.Contains(t, string(text), "SYNTHETIC-ENUM")
				}
			}
		})
	}
	// Entries are named in declaration order, whatever order a map holds
	// them in: the first policy met takes the type's name.
	for name, tc := range map[string]struct {
		doc  map[string]any
		refs map[string]string
	}{
		"live first": {jsonSchemaOf[schemaLiveFirst](t), map[string]string{
			"live": "config.schemaRecNode", "restart": "config.schemaRecNode_2", "hidden": "config.schemaRecNode_3", "again": "config.schemaRecNode",
		}},
		"restart first": {jsonSchemaOf[schemaRestartFirst](t), map[string]string{
			"restart": "config.schemaRecNode", "hidden": "config.schemaRecNode_2", "live": "config.schemaRecNode_3", "again": "config.schemaRecNode_3",
		}},
	} {
		for key, def := range tc.refs {
			assert.Equal(t, "#/$defs/"+def, prop(t, tc.doc, key)["$ref"], name+": "+key)
		}
	}
}

func mustFieldFor[T any](t *testing.T, key string) Field {
	t.Helper()
	f, err := FieldFor[T](key)
	require.NoError(t, err)
	return f
}

type (
	schemaRecList []schemaRecList
	schemaRecMap  map[string]schemaRecMap
	schemaRing    [2]*schemaRing
	schemaKids    []schemaKid
	schemaKid     struct {
		Name string     `koanf:"name"`
		Kids schemaKids `koanf:"kids"`
	}
)

type schemaRecCollections struct {
	List     schemaRecList  `koanf:"list" validate:"max=3"`
	Map      schemaRecMap   `koanf:"map" validate:"min=1"`
	Ring     schemaRing     `koanf:"ring"`
	Kids     schemaKids     `koanf:"kids"`
	Plain    []int          `koanf:"plain" validate:"max=2"`
	PlainMap map[string]int `koanf:"plain_map"`
	Secret   schemaRecList  `koanf:"secret" sensitive:"true"`
}

// A named list or map holding itself, directly or through a struct, is a
// $defs entry like a recursive struct: the schema is finite and every
// reference resolves. A key's own rules still bound it, a fixed array keeps
// its length and a sensitive one describes nothing inside.
func TestJSONSchema_RecursiveCollectionsAreReferenced(t *testing.T) {
	// A renderer that recursed without end would overflow this stack at
	// once rather than run until the test times out.
	defer debug.SetMaxStack(debug.SetMaxStack(32 << 20))
	doc := jsonSchemaOf[schemaRecCollections](t)
	assertRefsResolve(t, doc, doc)

	list := nonNull(t, prop(t, doc, "list"))
	assert.Equal(t, map[string]any{"$ref": "#/$defs/config.schemaRecList", "type": "array", "maxItems": 3.0}, list)
	assert.Equal(t, map[string]any{"type": "array", "items": map[string]any{"anyOf": []any{
		map[string]any{"$ref": "#/$defs/config.schemaRecList", "type": "array"}, map[string]any{"type": "null"}}}}, resolved(t, doc, list))

	m := nonNull(t, prop(t, doc, "map"))
	assert.Equal(t, map[string]any{"$ref": "#/$defs/config.schemaRecMap", "type": "object", "minProperties": 1.0}, m)
	assert.Equal(t, map[string]any{"type": "object", "additionalProperties": map[string]any{"anyOf": []any{
		map[string]any{"$ref": "#/$defs/config.schemaRecMap", "type": "object"}, map[string]any{"type": "null"}}}}, resolved(t, doc, m))

	ring := resolved(t, doc, prop(t, doc, "ring"))
	assert.Equal(t, 2.0, ring["minItems"])
	assert.Equal(t, 2.0, ring["maxItems"])
	assert.Equal(t, "#/$defs/config.schemaRing", nonNull(t, ring["items"].(map[string]any))["$ref"])

	kids := resolved(t, doc, nonNull(t, prop(t, doc, "kids")))
	kid := kids["items"].(map[string]any)
	assert.Equal(t, "object", kid["type"], "the struct inside the cycle is written in place")
	assert.Equal(t, "#/$defs/config.schemaKids", nonNull(t, prop(t, kid, "kids"))["$ref"])

	assert.Equal(t, map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "maxItems": 2.0}, nonNull(t, prop(t, doc, "plain")))
	assert.Equal(t, map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "integer"}}, nonNull(t, prop(t, doc, "plain_map")))
	assert.Equal(t, map[string]any{"type": "array"}, nonNull(t, prop(t, doc, "secret")))
	assert.Equal(t, true, prop(t, doc, "secret")["writeOnly"])
	assert.ElementsMatch(t, []string{"config.schemaRecList", "config.schemaRecMap", "config.schemaRing", "config.schemaKids"}, slices.Collect(maps.Keys(doc["$defs"].(map[string]any))))
}
