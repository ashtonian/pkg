//go:build unit

package config

import (
	"encoding/json"
	"net/url"
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
		"description": "Requests per second", "x-config-validation": []any{"min", "max"}}, prop(t, elem, "rate"),
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
	assert.NotContains(t, mode, "minLength", "omitempty exempts an empty value, which JSON Schema cannot say")
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
	assert.Equal(t, map[string]any{"type": "string", "writeOnly": true, "minLength": 8.0, "x-config-validation": []any{"min", "oneof"}}, password)
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
