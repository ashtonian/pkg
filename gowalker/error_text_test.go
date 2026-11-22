package gowalker_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ashtonian/pkg/gowalker"
)

// secretMarker stands for a secret a tag holds, such as a default password.
const secretMarker = "SECRET-MARKER"

type secretDuplicateKey struct {
	//lint:ignore SA5008 the repeated key is what compilation refuses
	Password string `default:"SECRET-MARKER" default:"x"`
}

type secretBox[T any] struct {
	V T
}

// secretType is an unnamed struct whose field's tag holds the marker, after
// characters reflect escapes when it prints the tag.
var secretType = reflect.StructOf([]reflect.StructField{
	{Name: "Password", Type: reflect.TypeFor[string](), Tag: `json:"password" default:"a\"b\\ SECRET-MARKER"`},
})

// The text of an error leaves out every struct tag, wherever the error
// formats a type or a tag: a malformed or repeated tag on a named or unnamed
// struct, a name conflict whose owner is unnamed, an unnamed struct in an
// error's type, and one in a generic type's arguments. The structured error
// keeps what a caller that inspects it needs.
func TestErrorTextOmitsTagValues(t *testing.T) {
	malformed := reflect.StructOf([]reflect.StructField{
		{Name: "Password", Type: reflect.TypeFor[string](), Tag: `default:"SECRET-MARKER" env: "X"`},
	})
	repeated := reflect.StructOf([]reflect.StructField{
		{Name: "Password", Type: reflect.TypeFor[string](), Tag: `default:"SECRET-MARKER" default:"x"`},
	})
	conflict := reflect.StructOf([]reflect.StructField{
		{Name: "A", Type: reflect.TypeFor[string](), Tag: `json:"same" default:"SECRET-MARKER"`},
		{Name: "B", Type: reflect.TypeFor[string](), Tag: `json:"same"`},
	})
	tagged := engine(t, gowalker.WithTaggedFields("json"))
	for name, tc := range map[string]struct {
		engine *gowalker.Engine
		typ    reflect.Type
		is     error
		want   string
		check  func(t *testing.T, err error)
	}{
		"repeated key on a named struct": {
			engine: gowalker.Default(), typ: reflect.TypeFor[secretDuplicateKey](), is: gowalker.ErrSchema,
			want: `field Password: struct tag repeats the key "default" at byte 24`,
			check: func(t *testing.T, err error) {
				tagErr, ok := errors.AsType[*gowalker.TagError](err)
				if !ok || tagErr.Key != "default" || tagErr.Offset != 24 || !strings.Contains(string(tagErr.Tag), secretMarker) {
					t.Errorf("TagError %#v", tagErr)
				}
			},
		},
		"malformed tag on an unnamed struct": {
			engine: gowalker.Default(), typ: malformed, is: gowalker.ErrSchema,
			want: "compile struct { Password string }: invalid schema: field Password: struct tag is malformed at byte 24",
			check: func(t *testing.T, err error) {
				if tagErr, ok := errors.AsType[*gowalker.TagError](err); !ok || tagErr.Offset != 24 || tagErr.Tag != malformed.Field(0).Tag {
					t.Errorf("TagError %#v", tagErr)
				}
			},
		},
		"repeated key on an unnamed struct": {
			engine: gowalker.Default(), typ: repeated, is: gowalker.ErrSchema,
			want: `compile struct { Password string }: invalid schema: field Password: struct tag repeats the key "default"`,
		},
		"name conflict on an unnamed owner": {
			engine: tagged, typ: conflict, is: gowalker.ErrSchema,
			want: `fields A and B of struct { A string; B string } both use the name "same"`,
			check: func(t *testing.T, err error) {
				if c, ok := errors.AsType[*gowalker.NameConflict](err); !ok || c.Owner != conflict || c.First.GoField().Tag.Get("default") != secretMarker {
					t.Errorf("NameConflict %#v", c)
				}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tc.engine.CompileType(tc.typ)
			if !errors.Is(err, tc.is) {
				t.Fatalf("compiled: %v", err)
			}
			if text := err.Error(); strings.Contains(text, secretMarker) || !strings.Contains(text, tc.want) {
				t.Errorf("text %q, want it to hold %q and no tag value", text, tc.want)
			}
			if tc.check != nil {
				tc.check(t, err)
			}
		})
	}
}

// An error that is not a compilation's formats the type the same way, and
// keeps the full type for a caller that inspects it.
func TestErrorTextOmitsTagsOfAnUnnamedRoot(t *testing.T) {
	schema, err := gowalker.CompileType(secretType)
	if err != nil {
		t.Fatal(err)
	}
	_, err = schema.ResolveText("missing")
	if !errors.Is(err, gowalker.ErrPath) {
		t.Fatalf("resolved: %v", err)
	}
	if text := err.Error(); strings.Contains(text, secretMarker) || !strings.Contains(text, "struct { Password string }") {
		t.Errorf("text %q", text)
	}
	if e, ok := errors.AsType[*gowalker.Error](err); !ok || e.Type != secretType {
		t.Errorf("Error %#v", e)
	}
	// A generic type's name holds its arguments' text.
	generic := reflect.TypeFor[secretBox[struct {
		Password string `default:"SECRET-MARKER"`
	}]]()
	schema, err = gowalker.CompileType(generic)
	if err != nil {
		t.Fatal(err)
	}
	_, err = schema.ResolveText("missing")
	if text := err.Error(); strings.Contains(text, secretMarker) || !strings.Contains(text, "secretBox[struct { Password string }]") {
		t.Errorf("text %q", text)
	}
	// The type's text runs from one quote to the next, skipping escaped ones.
	pair := reflect.StructOf([]reflect.StructField{
		{Name: "A", Type: secretType, Tag: `note:"\"quoted\" SECRET-MARKER"`},
		{Name: "B", Type: reflect.TypeFor[map[string]int]()},
	})
	schema, err = gowalker.CompileType(pair)
	if err != nil {
		t.Fatal(err)
	}
	_, err = schema.ResolveText("missing")
	if text := err.Error(); strings.Contains(text, secretMarker) || !strings.Contains(text, "struct { A struct { Password string }; B map[string]int }") {
		t.Errorf("text %q", text)
	}
}
