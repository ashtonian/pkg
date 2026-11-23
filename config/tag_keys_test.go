package config

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/ashtonian/pkg/gowalker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The keys config reads are far enough from other libraries' keys that
// none is taken for a misspelling.
func TestTagKeys_Misspellings(t *testing.T) {
	tests := []struct {
		written string
		meant   string
	}{
		{"defualt", "default"},
		{"vaildate", "validate"},
		{"konaf", "koanf"},
		{"sensitve", "sensitive"},
		{"env_ref", "envref"},
		{"DESC", "desc"},
		{"secert", "secret"},
		{"exmaple", "example"},
		{"sep", ""},
		{"json", ""},
		{"yaml", ""},
		{"mapstructure", ""},
		{"help", ""},
		{"short", ""},
		{"enum", ""},
		{"doc", ""},
		{"form", ""},
	}
	for _, tt := range tests {
		t.Run(tt.written, func(t *testing.T) {
			meant, ok := gowalker.MisspeltTagKey(tt.written, tagKeys)
			assert.Equal(t, tt.meant, meant)
			assert.Equal(t, tt.meant != "", ok)
		})
	}
}

type misspeltBase struct {
	Region string `koanf:"region" defualt:"eu"`
}

type misspeltItem struct {
	Port int `koanf:"port" vaildate:"min=1"`
}

type misspeltCfg struct {
	misspeltBase `koanf:",squash"`

	Host  string         `koanf:"host" json:"host" yaml:"host" help:"the host"`
	Items []misspeltItem `koanf:"items"`
}

// foreignBase lists the key another library reads on its field.
type foreignBase struct {
	Region string `koanf:"region" defualt:"eu"`
}

func (foreignBase) ForeignTagKeys() []string {
	keys := []string{"defualt"}
	return keys
}

type foreignItem struct {
	Port int `koanf:"port" vaildate:"min=1"`
}

func (*foreignItem) ForeignTagKeys() []string {
	keys := []string{"vaildate"}
	return keys
}

// foreignOther carries the key its own type does not list: foreignBase's
// list covers foreignBase's fields only.
type foreignOther struct {
	Other string `koanf:"other" defualt:"x"`
}

type foreignCfg struct {
	foreignBase `koanf:",squash"`

	Host  string        `koanf:"host" defualt:"inherited"`
	Items []foreignItem `koanf:"items"`
	More  foreignOther  `koanf:"more"`
}

type partlyForeignCfg struct {
	foreignBase `koanf:",squash"`
	Items       []misspeltItem `koanf:"items"`
}

// A load refuses every misspelt key of its type's graph, through squashed
// structs and list elements, naming the field, the key written and the key
// meant, unless the struct declaring the field lists it as another
// library's.
func TestLoad_RefusesMisspeltTagKeys(t *testing.T) {
	_, err := Load[misspeltCfg](t.Context(), WithSkipValidation())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), `field Region of config.misspeltBase has tag key "defualt"; did you mean "default"? List a key another library reads in config.misspeltBase's ForeignTagKeys method`)
	assert.Contains(t, err.Error(), `field Port of config.misspeltItem has tag key "vaildate"; did you mean "validate"?`)
	assert.NotContains(t, err.Error(), `"json"`)

	err = CheckKey[partlyForeignCfg](t.Context(), "region", "h")
	require.ErrorIs(t, err, ErrSchema)
	assert.NotContains(t, err.Error(), "defualt")
	assert.Contains(t, err.Error(), "vaildate")

	_, err = Load[foreignCfg](t.Context(), WithSkipValidation())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), `field Other of config.foreignOther has tag key "defualt"`)
	assert.Contains(t, err.Error(), `field Host of config.foreignCfg has tag key "defualt"`, "an embedding struct is promoted the method, not the allowance")
	assert.NotContains(t, err.Error(), "Region")
	assert.NotContains(t, err.Error(), "vaildate")

	type allForeign struct {
		foreignBase `koanf:",squash"`
		Items       []foreignItem `koanf:"items"`
	}
	got, err := Load[allForeign](t.Context(), WithDefaults(map[string]any{"items": []any{map[string]any{"port": 0}}}), WithSkipValidation())
	require.NoError(t, err)
	assert.Empty(t, got.Region, "a foreign key is not config's")
	assert.Equal(t, 0, got.Items[0].Port)
}

// A tag reflect cannot read whole, or one that repeats a key, is refused
// before anything loads: reflect would read only part of it. The types are
// built at run time, as vet refuses such tags in source.
func TestLoad_RefusesUnreadableTags(t *testing.T) {
	for _, tag := range []reflect.StructTag{`koanf:port default:"8"`, `koanf:"port" default:"8" default:"9"`} {
		t.Run(string(tag), func(t *testing.T) {
			typ := reflect.StructOf([]reflect.StructField{{Name: "Port", Type: reflect.TypeFor[int](), Tag: tag}})
			_, err := loadSchema(typ, buildOptions(nil))
			require.ErrorIs(t, err, ErrSchema)
			assert.Contains(t, err.Error(), "Port")
		})
	}
}

type repeatedSecretCfg struct {
	//lint:ignore SA5008 the repeated key is what a load refuses
	Password string `koanf:"password" sensitive:"true" default:"SECRET-MARKER" default:"x"`
}

// A schema error names the field and the tag key, never a tag's value: a
// default on a sensitive field is the secret itself. An unnamed section's
// type prints without its tags.
func TestLoad_SchemaErrorsLeaveTagValuesOut(t *testing.T) {
	_, err := Load[repeatedSecretCfg](t.Context(), WithSkipValidation())
	require.ErrorIs(t, err, ErrSchema)
	assert.NotContains(t, err.Error(), "SECRET-MARKER")
	assert.Contains(t, err.Error(), `field Password: struct tag repeats the key "default"`)

	for _, tag := range []reflect.StructTag{
		`koanf:"token" default:"SECRET-MARKER" env: "TOKEN"`,
		`koanf:"token" default:"SECRET-MARKER" default:"x"`,
	} {
		section := reflect.StructOf([]reflect.StructField{{Name: "Token", Type: reflect.TypeFor[string](), Tag: tag}})
		root := reflect.StructOf([]reflect.StructField{
			{Name: "Auth", Type: section, Tag: `koanf:"auth"`},
			{Name: "Other", Type: reflect.TypeFor[string](), Tag: `koanf:"other" default:"SECRET-MARKER"`},
		})
		_, err := loadSchema(root, buildOptions(nil))
		require.ErrorIs(t, err, ErrSchema)
		assert.NotContains(t, err.Error(), "SECRET-MARKER")
		assert.Contains(t, err.Error(), "field Token")
	}
}

type sharedDeclarations struct {
	Name string `koanf:"name" defualt:"x" sensitive:"maybe"`
}

type recursiveDeclarations struct {
	Name     string                           `koanf:"name"`
	Children []recursiveDeclarations          `koanf:"children"`
	ByName   map[string]recursiveDeclarations `koanf:"by_name"`
}

type sharedDeclarationsCfg struct {
	misspeltBase `koanf:",squash"`

	First  sharedDeclarations            `koanf:"first"`
	Second *sharedDeclarations           `koanf:"second"`
	Items  []misspeltItem                `koanf:"items"`
	Named  map[string]sharedDeclarations `koanf:"named"`
	Tree   recursiveDeclarations         `koanf:"tree"`
}

// The declaration checks read every struct of a graph once, whatever the
// paths that reach it: a type held at several paths, behind a pointer or in
// a collection is one set of declarations, and a recursive type ends.
func TestDeclarationChecks_VisitEachStructOnce(t *testing.T) {
	root, err := metadataOf(reflect.TypeFor[sharedDeclarationsCfg]())
	require.NoError(t, err)
	var visited []string
	root.eachBranch(func(n *schemaNode) {
		visited = append(visited, gowalker.TypeText(n.view.Root().Type()))
	})
	assert.Equal(t, []string{
		"config.sharedDeclarationsCfg", "config.sharedDeclarations", "config.misspeltItem", "config.recursiveDeclarations",
	}, visited)

	_, err = declaredOf(reflect.TypeFor[sharedDeclarationsCfg]())
	require.ErrorIs(t, err, ErrSchema)
	var written []string
	for _, line := range strings.Split(err.Error(), "\n") {
		field, _, _ := strings.Cut(strings.TrimPrefix(line, ErrSchema.Error()+": field "), " has tag key")
		key, _, _ := strings.Cut(strings.SplitN(line, `has tag key "`, 2)[1], `"`)
		written = append(written, strings.Fields(field)[0]+":"+key)
	}
	assert.Equal(t, []string{"Region:defualt", "Name:defualt", "Port:vaildate"}, written, "squashed and element declarations are checked, a shared one once")

	_, err = rootSchema(reflect.TypeFor[sharedDeclarationsCfg]())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), `field Name of config.sharedDeclarations has sensitive:"maybe"`)
}

type ambiguousSensitivity struct {
	Note string `koanf:"note" sensitve:"true"`
}

type misspeltDefault struct {
	Port int `koanf:"port" defualt:"80"`
}

type foreignDefault struct {
	Port int `koanf:"port" defualt:"80"`
}

func (foreignDefault) ForeignTagKeys() []string {
	keys := []string{"defualt"}
	return keys
}

type plainSensitivity struct {
	Note string `koanf:"note" sensitive:"true"`
	Port int    `koanf:"port" default:"80"`
}

const harmlessMarker = "HARMLESS-MARKER"

// Checked redaction holds a type's declarations to the policy a load does:
// a value whose sensitivity declaration is misspelt is never printed, by
// the checked helpers, their compatibility wrappers or any print format,
// whoever populated it.
func TestDeclarationPolicy_AmbiguousSensitivityNeverPrints(t *testing.T) {
	live := ambiguousSensitivity{Note: harmlessMarker}
	_, err := Load[ambiguousSensitivity](t.Context(), WithDefaults(map[string]any{"note": harmlessMarker}))
	require.ErrorIs(t, err, ErrSchema)
	want := err.Error()

	m, err := RedactedMapChecked(live)
	require.ErrorIs(t, err, ErrSchema)
	assert.Equal(t, want, "loading config: "+err.Error())
	assert.Nil(t, m)
	assert.Nil(t, RedactedMap(live))
	assert.Nil(t, RedactedMap(&live))
	tree, err := RedactedTreeChecked(live)
	require.ErrorIs(t, err, ErrSchema)
	assert.Nil(t, tree)
	assert.Nil(t, RedactedTree(live))
	for _, format := range []string{FormatTable, FormatJSON, FormatYAML} {
		var out bytes.Buffer
		require.ErrorIs(t, PrintRedacted(live, WithWriter(&out), WithFormat(format)), ErrSchema, format)
		assert.Empty(t, out.String(), format)
	}
	var out bytes.Buffer
	require.ErrorIs(t, PrintEnv(live, "APP", WithWriter(&out)), ErrSchema)
	assert.Empty(t, out.String())

	type holder struct {
		Plugin any `koanf:"plugin"`
	}
	m, err = RedactedMapChecked(holder{Plugin: live})
	require.ErrorIs(t, err, ErrSchema, "a struct held in an interface is checked as it is reached")
	assert.Nil(t, m)

	// Raw inspection still reads the type, to diagnose it.
	fields, err := FieldsChecked[ambiguousSensitivity]()
	require.NoError(t, err)
	assert.Equal(t, "note", fields[0].Path)
	raw, err := Tree(live)
	require.NoError(t, err)
	assert.Equal(t, harmlessMarker, raw["note"])
}

// Every generator an operator reads agrees with a load on a misspelt tag
// key, and on a key the declaring type lists as another library's.
func TestDeclarationPolicy_GeneratorsAgreeWithLoad(t *testing.T) {
	_, loadErr := Load[misspeltDefault](t.Context(), WithSkipValidation())
	require.ErrorIs(t, loadErr, ErrSchema)
	assert.Contains(t, loadErr.Error(), `field Port of config.misspeltDefault has tag key "defualt"; did you mean "default"?`)
	var out bytes.Buffer
	for name, generate := range map[string]func() error{
		"Usage":         func() error { _, err := Usage[misspeltDefault]("APP"); return err },
		"PrintUsage":    func() error { return PrintUsage[misspeltDefault]("APP", WithWriter(&out)) },
		"JSONSchema":    func() error { _, err := JSONSchema[misspeltDefault](); return err },
		"PrintTemplate": func() error { return PrintTemplate[misspeltDefault]("APP", WithWriter(&out)) },
		"PrintEnv":      func() error { return PrintEnv(misspeltDefault{}, "APP", WithWriter(&out)) },
		"Diff":          func() error { _, err := Diff(&misspeltDefault{}, &misspeltDefault{Port: 1}); return err },
		"CanonicalKey":  func() error { _, err := CanonicalKey[misspeltDefault]("port"); return err },
		"DecodeEdits": func() error {
			_, err := DecodeEdits[misspeltDefault](t.Context(), nil, map[string]any{"port": 1})
			return err
		},
		"NewReloader": func() error {
			_, err := NewReloader[misspeltDefault](t.Context(), []Option{WithSkipValidation()})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			out.Reset()
			err := generate()
			require.ErrorIs(t, err, ErrSchema)
			assert.Contains(t, err.Error(), `field Port of config.misspeltDefault has tag key "defualt"`)
			assert.Empty(t, out.String())
		})
	}

	got, err := Load[foreignDefault](t.Context(), WithSkipValidation())
	require.NoError(t, err)
	assert.Zero(t, got.Port, "the foreign key is not config's default")
	rows, err := Usage[foreignDefault]("APP")
	require.NoError(t, err)
	assert.Empty(t, rows[0].Default)
	doc := jsonSchemaOf[foreignDefault](t)
	assert.NotContains(t, prop(t, doc, "port"), "default")
	m, err := RedactedMapChecked(foreignDefault{Port: 8})
	require.NoError(t, err)
	assert.Equal(t, "8", m["port"])
	require.NoError(t, PrintTemplate[foreignDefault]("APP", WithWriter(&out)))
}

// A valid schema keeps its redaction and its metadata, and a live value
// still prints without its tags decoding.
func TestDeclarationPolicy_ValidSchemasUnchanged(t *testing.T) {
	m, err := RedactedMapChecked(plainSensitivity{Note: harmlessMarker, Port: 1})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"note": "[REDACTED]", "port": "1"}, m)
	rows, err := Usage[plainSensitivity]("APP")
	require.NoError(t, err)
	assert.Equal(t, "80", rows[1].Default)
	got, err := Load[plainSensitivity](t.Context(), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, 80, got.Port)

	type badDefault struct {
		Labels map[string]string `koanf:"labels" default:"not-entries"`
	}
	m, err = RedactedMapChecked(badDefault{Labels: map[string]string{"k": "v"}})
	require.NoError(t, err, "printing a live value does not decode its defaults")
	assert.Equal(t, "v", m[`labels["k"]`])
	_, loadErr := Load[badDefault](t.Context(), WithSkipValidation())
	require.ErrorIs(t, loadErr, ErrSchema)
	_, err = Usage[badDefault]("APP")
	require.ErrorIs(t, err, ErrSchema, "usage shows the default a load refuses")
	assert.Equal(t, loadErr.Error(), "loading config: "+err.Error())
	_, err = JSONSchema[badDefault]()
	require.ErrorIs(t, err, ErrSchema)
}

// foreignColumn lists a key another library reads on its own field.
type foreignColumn struct {
	Name string `koanf:"name" defaults:"id"`
}

func (foreignColumn) ForeignTagKeys() []string {
	keys := []string{"defaults"}
	return keys
}

type foreignPointerColumn struct {
	Name string `koanf:"name" defaults:"id"`
}

func (*foreignPointerColumn) ForeignTagKeys() []string {
	keys := []string{"defaults"}
	return keys
}

// promotedSquashed, promotedSection and promotedPointer embed a declarer and
// misspell default on a field of their own.
type promotedSquashed struct {
	foreignColumn `koanf:",squash"`
	Port          int `koanf:"port" defaults:"80"`
}

type promotedSection struct {
	foreignColumn `koanf:"col"`
	Port          int `koanf:"port" defaults:"80"`
}

type promotedPointer struct {
	foreignPointerColumn `koanf:",squash"`
	Port                 int `koanf:"port" defaults:"80"`
}

// shadowingDeclarer embeds a declarer and declares a list of its own.
type shadowingDeclarer struct {
	foreignColumn `koanf:",squash"`
	Port          int `koanf:"port" defaults:"80"`
}

func (shadowingDeclarer) ForeignTagKeys() []string {
	keys := []string{"defaults"}
	return keys
}

type genericDeclarer[T any] struct {
	Value T `koanf:"value" defaults:"v"`
}

func (genericDeclarer[T]) ForeignTagKeys() []string {
	keys := []string{"defaults"}
	return keys
}

// A ForeignTagKeys list covers the fields of the struct declaring the
// method, wherever they are reached. A struct embedding a declarer is
// promoted the method but not the allowance, so a misspelt key on its own
// field is refused rather than load the field as its zero value; a struct
// declaring its own method, a generic one included, covers its fields.
func TestForeignTagKeys_CoverTheDeclaringStructOnly(t *testing.T) {
	for name, load := range map[string]func() error{
		"squashed":       func() error { _, err := Load[promotedSquashed](t.Context(), WithSkipValidation()); return err },
		"section":        func() error { _, err := Load[promotedSection](t.Context(), WithSkipValidation()); return err },
		"pointer method": func() error { _, err := Load[promotedPointer](t.Context(), WithSkipValidation()); return err },
	} {
		t.Run(name, func(t *testing.T) {
			err := load()
			require.ErrorIs(t, err, ErrSchema)
			assert.Contains(t, err.Error(), `field Port of config.`)
			assert.Contains(t, err.Error(), `has tag key "defaults"; did you mean "default"?`)
			assert.NotContains(t, err.Error(), "field Name", "the declarer's own field stays covered")
		})
	}
	got, err := Load[shadowingDeclarer](t.Context(), WithSkipValidation())
	require.NoError(t, err)
	assert.Zero(t, got.Port, "another library's key is not config's")
	_, err = Load[genericDeclarer[string]](t.Context(), WithSkipValidation())
	require.NoError(t, err)
}

// A TreeFunc callback judges each leaf by its Field, so a misspelt
// sensitivity tag would hand the documented redaction recipe a value it
// takes for ordinary. With a callback, TreeFunc refuses the type as checked
// redaction does, wherever the type is reached; Tree, which withholds
// nothing, still exports it.
func TestTreeFunc_CallbackRefusesMisspeltTagKeys(t *testing.T) {
	live := ambiguousSensitivity{Note: harmlessMarker}
	redact := func(_ string, f Field, v any) any {
		if f.Sensitive && v != nil {
			return redactedValue
		}
		return v
	}
	type sensitiveHolder struct {
		Notes map[string]ambiguousSensitivity `koanf:"notes" sensitive:"true"`
	}
	type dynamicHolder struct {
		Plugin any `koanf:"plugin"`
	}
	for name, cfg := range map[string]any{
		"root":              live,
		"sensitive map":     sensitiveHolder{Notes: map[string]ambiguousSensitivity{"a": live}},
		"interface's value": dynamicHolder{Plugin: live},
	} {
		tree, err := TreeFunc(cfg, redact)
		require.ErrorIs(t, err, ErrSchema, name)
		assert.Nil(t, tree, name)
	}

	raw, err := TreeFunc(live, nil)
	require.NoError(t, err)
	assert.Equal(t, harmlessMarker, raw["note"])
}
