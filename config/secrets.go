package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-viper/mapstructure/v2"
)

// ErrSecretNotFound is what a [SecretsFrom] fetch function wraps when the
// store has no secret under the ref, so an optional secret can be left unset.
var ErrSecretNotFound = errors.New("secret not found")

// SecretsFrom returns a [SecretPopulator] that sets every field tagged
// secret:"<ref>" to what fetch returns for ref, decoded into the field's type
// as a source value is decoded — a string, an int, a duration:
//
//	type DBConfig struct {
//	    Host     string `koanf:"host" default:"localhost"`
//	    Password string `koanf:"password" secret:"prod/db#password"`
//	    User     string `koanf:"user" secret:"prod/db#username"`
//	    APIKey   string `koanf:"api_key" secret:"prod/api-key,optional"`
//	}
//
//	sm := secretsmanager.NewFromConfig(awsCfg)
//	cfg, err := config.Load[AppConfig](ctx, config.WithSecretPopulator(config.SecretsFrom(
//	    func(ctx context.Context, ref string) (string, error) {
//	        out, err := sm.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &ref})
//	        var missing *types.ResourceNotFoundException
//	        if errors.As(err, &missing) {
//	            return "", fmt.Errorf("%w: %s", config.ErrSecretNotFound, ref)
//	        }
//	        if err != nil {
//	            return "", err
//	        }
//	        return aws.ToString(out.SecretString), nil
//	    })))
//
// A tag is ref[#field][,optional]. With #field the secret is a JSON object and
// the field takes that member — a store that keeps a database's username and
// password in one secret is fetched once for both. With ,optional a fetch
// error wrapping [ErrSecretNotFound], or a missing member, leaves the field
// as the sources set it, so a development file can supply what production
// keeps in the store. A tag of "-" names no secret.
//
// The fetched value replaces whatever the sources set. fetch runs once per
// ref per load, with the load's context, in declaration order. Any other
// failure fails the load with [ErrSecretProvider], naming the key and the ref
// and wrapping fetch's error; a value that does not decode into its field is
// reported by type, never quoted. A field tagged secret is sensitive in
// every output, like sensitive:"true". A field behind a nil pointer — a
// section no source configured — is left alone. A secret tag inside a map or
// list cannot name one ref for every entry, and fails the load.
func SecretsFrom(fetch func(ctx context.Context, ref string) (string, error)) SecretPopulator {
	return func(ctx context.Context, target any) error {
		v := reflect.ValueOf(target)
		if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
			return fmt.Errorf("config: SecretsFrom needs a pointer to a config struct, got %s", typeText(reflect.TypeOf(target)))
		}
		root, err := rootSchema(v.Elem().Type())
		if err != nil {
			return err
		}
		s := &secretSetter{fetch: fetch, fetched: map[string]fetchedSecret{}}
		s.branch(ctx, nil, root, v.Elem())
		return s.err
	}
}

type secretSetter struct {
	fetch   func(ctx context.Context, ref string) (string, error)
	fetched map[string]fetchedSecret
	err     error
}

// fetchedSecret is one ref's fetch: its value, or that the store has none,
// and its JSON members once a #field asked for them.
type fetchedSecret struct {
	value    string
	notFound bool
	members  map[string]any
}

// secretTag is a parsed secret:"ref[#field][,optional]" tag.
type secretTag struct {
	ref, field string
	optional   bool
}

func parseSecretTag(tag string) (secretTag, bool) {
	if tag == "" || tag == "-" {
		return secretTag{}, false
	}
	spec, opts, _ := strings.Cut(tag, ",")
	t := secretTag{optional: strings.TrimSpace(opts) == "optional"}
	t.ref, t.field, _ = strings.Cut(spec, "#")
	return t, true
}

// branch sets the secrets of struct value v, at key p.
func (s *secretSetter) branch(ctx context.Context, p keyPath, n *schemaNode, v reflect.Value) {
	for _, f := range n.fields {
		if s.err != nil {
			return
		}
		key := p.field(f.key)
		if tag, ok := parseSecretTag(f.sf.Tag.Get("secret")); ok {
			if fv, err := v.FieldByIndexErr(f.index); err == nil {
				s.set(ctx, key, tag, fv)
			}
			continue
		}
		switch f.node.kind {
		case nodeBranch:
			fv, err := v.FieldByIndexErr(f.index)
			if err != nil {
				continue // behind a nil pointer: a section no source configured
			}
			if fv = derefValue(fv); fv.Kind() == reflect.Struct {
				s.branch(ctx, key, f.node, fv)
			}
		case nodeMap, nodeList:
			if at, ref, ok := secretBeneath(key.any(), f.node.elem, map[*schemaNode]bool{}); ok {
				s.err = fmt.Errorf("config: %s has secret:%q inside a map or list, where one ref cannot name every entry", at, ref)
			}
		}
	}
}

// secretBeneath finds a secret tag anywhere beneath n, at key p.
func secretBeneath(p keyPath, n *schemaNode, seen map[*schemaNode]bool) (keyPath, string, bool) {
	if seen[n] {
		return nil, "", false
	}
	seen[n] = true
	switch n.kind {
	case nodeBranch:
		for _, f := range n.fields {
			if tag, ok := parseSecretTag(f.sf.Tag.Get("secret")); ok {
				return p.field(f.key), tag.ref, true
			}
			if at, ref, ok := secretBeneath(p.field(f.key), f.node, seen); ok {
				return at, ref, true
			}
		}
	case nodeMap, nodeList:
		return secretBeneath(p.any(), n.elem, seen)
	}
	return nil, "", false
}

func (s *secretSetter) set(ctx context.Context, key keyPath, tag secretTag, field reflect.Value) {
	fail := func(err error) { s.err = fmt.Errorf("resolving secret %q for %s: %w", tag.ref, key, err) }
	got, ok := s.fetched[tag.ref]
	if !ok {
		value, err := s.fetch(ctx, tag.ref)
		switch {
		case errors.Is(err, ErrSecretNotFound):
			got.notFound = true
		case err != nil:
			fail(err)
			return
		default:
			got.value = value
		}
		s.fetched[tag.ref] = got
	}
	if got.notFound {
		if !tag.optional {
			fail(ErrSecretNotFound)
		}
		return
	}
	value := got.value
	if tag.field != "" {
		if got.members == nil {
			if err := json.Unmarshal([]byte(got.value), &got.members); err != nil || got.members == nil {
				fail(errors.New("the secret is not a JSON object, so #" + tag.field + " names nothing in it"))
				return
			}
			s.fetched[tag.ref] = got
		}
		member, ok := got.members[tag.field]
		switch {
		case !ok && tag.optional:
			return
		case !ok:
			fail(fmt.Errorf("the secret has no member %q", tag.field))
			return
		}
		value = memberText(member)
	}
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		DecodeHook:       (&options{}).decodeHook(nil),
		WeaklyTypedInput: true,
		Result:           field.Addr().Interface(),
	})
	if err == nil {
		err = dec.Decode(value)
	}
	if err != nil {
		// The decoder's message may quote the secret: name the type only.
		fail(errors.New("the secret does not decode into " + typeText(field.Type())))
	}
}

// memberText is a JSON member as the text a field decodes from: a string as
// itself, anything else as its JSON.
func memberText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}
