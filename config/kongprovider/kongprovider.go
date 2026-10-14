// Package kongprovider brings Kong CLI flags into config as its
// highest-precedence source, and describes the config struct in Kong's help.
//
// It lives in its own module so that config does not depend on
// alecthomas/kong; only callers who import kongprovider take that dependency.
//
// Only flags the user typed on the command line are forwarded, in any
// spelling Kong accepts: long, short, alias, `--flag=value`, and negated
// booleans (`--no-flag`). Kong defaults and values Kong resolved from its own
// resolvers are not forwarded, so they never override a config file or an
// environment variable — the config struct's `default` tags are the defaults.
//
// The simplest wiring embeds the config struct in the CLI struct: Kong makes
// a flag of each field, and [WithOverridesFromStruct] maps every flag to the
// key of the field it writes, so the config struct is the whole schema.
//
//	type AppConfig struct {
//	    Port  int  `koanf:"port" default:"8080" short:"p" desc:"HTTP listen port"`
//	    Debug bool `koanf:"debug" negatable:""`
//	}
//	type CLI struct {
//	    Config    string `type:"path" default:"config.yaml" help:"Config file."`
//	    AppConfig `embed:""`
//	}
//
//	var cli CLI
//	kctx := kong.Parse(&cli, kongprovider.HelpFromConfig[AppConfig]("APP"))
//	cfg, err := config.Load[AppConfig](ctx,
//	    config.WithFile(cli.Config),
//	    config.WithEnvPrefix("APP"),
//	    kongprovider.WithOverridesFromStruct[AppConfig](kctx),
//	)
package kongprovider

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"

	"github.com/ashtonian/pkg/config"
)

// New returns a koanf provider over the flags the user typed. mapping maps a
// Kong flag name to the koanf dot-path it sets ("port" → "server.port").
//
// Kong records each flag it parses from argv as an element of ctx.Path, under
// the flag's canonical name whatever spelling was typed, with the value
// already applied — so `--no-feature` arrives as feature=false. Elements a
// resolver supplied are marked Resolved, and defaults never appear: that is
// exactly the "typed by the user" distinction. Flag.Set cannot be used for
// it: Kong sets that for applied defaults too.
//
// Values unflatten into the key tree, so a flag mapped to "listeners.tcp"
// overrides that nested key instead of adding a literal dotted top-level key.
// A mapping key that names no flag anywhere in the CLI — a typo, or a flag
// since renamed — makes Read fail, so the load fails with config.ErrSource
// instead of the override silently never applying.
//
// Pass the provider to config.WithOverrideProvider with a nil parser, or use
// [WithOverrides].
func New(ctx *kong.Context, mapping map[string]string) koanf.Provider {
	return provider{ctx: ctx, mapping: mapping}
}

type provider struct {
	ctx     *kong.Context
	mapping map[string]string
	err     error
}

func (p provider) Read() (map[string]any, error) {
	if p.err != nil {
		return nil, p.err
	}
	if unknown := unknownFlags(p.ctx.Model.Node, p.mapping); len(unknown) > 0 {
		return nil, fmt.Errorf("kongprovider: the CLI has no flag named %s", strings.Join(unknown, ", "))
	}
	values := make(map[string]any)
	for _, el := range p.ctx.Path {
		if el.Flag == nil || el.Resolved {
			continue
		}
		if key, ok := p.mapping[el.Flag.Name]; ok {
			values[key] = el.Flag.Target.Interface()
		}
	}
	return confmap.Provider(values, ".").Read()
}

func (provider) ReadBytes() ([]byte, error) {
	return nil, errors.New("kongprovider: the provider returns a map; load it without a parser")
}

// WithOverrides returns a config.Option that loads [New]'s provider as an
// override, above the environment.
//
// A flag name is one key for the whole CLI. When two subcommands give one
// flag name different meanings (`serve --port` and `admin --port`), build
// the mapping for the command that was selected:
//
//	mapping := map[string]string{"port": "server.port"}
//	if kctx.Command() == "admin" {
//	    mapping = map[string]string{"port": "admin.port"}
//	}
//	cfg, err := config.Load[AppConfig](ctx, kongprovider.WithOverrides(kctx, mapping))
func WithOverrides(ctx *kong.Context, mapping map[string]string) config.Option {
	return config.WithOverrideProvider(New(ctx, mapping), nil)
}

// WithOverridesFromTags is [WithOverrides] with the mapping read from
// `flag:"name"` tags on T, keyed by the paths [config.Fields] reports — the
// same keys defaults and environment variables set. Use it when the CLI
// struct is separate from the config struct. Two fields tagged with one flag
// name fail the load.
func WithOverridesFromTags[T any](ctx *kong.Context) config.Option {
	mapping, err := flagMapping[T]()
	return config.WithOverrideProvider(provider{ctx: ctx, mapping: mapping, err: err}, nil)
}

// WithOverridesFromStruct is [WithOverrides] for a CLI struct that embeds the
// config struct T (`embed:""`), or is T: every Kong flag that writes a field
// of T overrides that field's key, with no mapping or `flag` tags to keep in
// step. Flags outside T — the config file's path, say — are the CLI's own and
// are not forwarded. A CLI struct holding no T fails the load.
func WithOverridesFromStruct[T any](ctx *kong.Context) config.Option {
	mapping, err := structMapping[T](ctx.Model.Node)
	return config.WithOverrideProvider(provider{ctx: ctx, mapping: mapping, err: err}, nil)
}

// HelpFromConfig returns a Kong option that describes each flag that sets a
// key of T — a flag a `flag` tag names, or one that writes a field of a T the
// CLI struct embeds. A flag with no help of its own takes the field's `desc`
// tag, and its help gains the config key it sets and the environment variable
// that sets the same key under envPrefix. The field's default is added when
// diagnostics may print it (see config.Field.PrintableDefault) and Kong does
// not show it already; one that must be withheld is kept out of Kong's own
// placeholder too. Kong fails to build the CLI when a `flag` tag names no
// flag, or when two fields name one.
//
// Declare no `required` on the CLI's own flags: Kong validates before the
// config pipeline runs, so a required flag could not be satisfied by a file,
// the environment or a secret. The config struct's validation, which runs
// after every source, is the one that applies.
func HelpFromConfig[T any](envPrefix string) kong.Option {
	return kong.PostBuild(func(k *kong.Kong) error {
		byPath := make(map[string]config.Field)
		for _, f := range config.Fields[T]() {
			byPath[f.Path] = f
		}
		mapping, err := flagMapping[T]()
		if err != nil {
			return err
		}
		flags := allFlags(k.Model.Node)
		for name, path := range mapping {
			if !slices.ContainsFunc(flags, func(fl *kong.Flag) bool { return fl.Name == name }) {
				return fmt.Errorf("kongprovider: %s is tagged flag %q, but the CLI has no such flag", path, name)
			}
		}
		if embedded, err := structMapping[T](k.Model.Node); err == nil {
			for name, path := range embedded {
				mapping[name] = path
			}
		}
		for _, fl := range flags {
			path, ok := mapping[fl.Name]
			if !ok {
				continue
			}
			describe(fl, byPath[path], envPrefix)
		}
		return nil
	})
}

// describe writes field f's description into flag fl's help.
func describe(fl *kong.Flag, f config.Field, envPrefix string) {
	if fl.Help == "" {
		fl.Help = f.Tag.Get("desc")
	}
	hints := []string{"config: " + f.Path}
	if env := f.EnvVar(envPrefix); env != "" {
		hints = append(hints, "env: "+env)
	}
	def, printable := f.PrintableDefault()
	switch {
	case printable && !fl.HasDefault:
		hints = append(hints, "default: "+def)
	case !printable && fl.HasDefault:
		fl.PlaceHolder = "VALUE" // Kong would print the default as the placeholder
	}
	fl.Help = strings.TrimSpace(fl.Help + " (" + strings.Join(hints, "; ") + ")")
}

// structMapping maps each Kong flag that writes a field of the T held by the
// CLI rooted at root to that field's config key.
func structMapping[T any](root *kong.Node) (map[string]string, error) {
	held, ok := findValue(root.Target, reflect.TypeFor[T]())
	if !ok {
		return nil, fmt.Errorf("kongprovider: the CLI struct %s holds no %s; embed it (embed:\"\") or use WithOverridesFromTags", root.Target.Type(), reflect.TypeFor[T]())
	}
	keys := make(map[fieldAddr]string)
	for _, f := range config.Fields[T]() {
		if fv, err := held.FieldByIndexErr(f.Index); err == nil && fv.CanAddr() {
			keys[fieldAddr{fv.Addr().Pointer(), fv.Type()}] = f.Path
		}
	}
	mapping := make(map[string]string)
	for _, fl := range allFlags(root) {
		if fl.Target.CanAddr() {
			if path, ok := keys[fieldAddr{fl.Target.Addr().Pointer(), fl.Target.Type()}]; ok {
				mapping[fl.Name] = path
			}
		}
	}
	return mapping, nil
}

// fieldAddr identifies a field by address and type: a struct and its first
// field share an address.
type fieldAddr struct {
	ptr uintptr
	typ reflect.Type
}

// findValue finds the value of type t in v — v itself, or a struct field
// within it, embedded or named, at any depth.
func findValue(v reflect.Value, t reflect.Type) (reflect.Value, bool) {
	for v.IsValid() && v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return reflect.Value{}, false
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return reflect.Value{}, false
	}
	if v.Type() == t {
		return v, true
	}
	if v.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	for i := range v.NumField() {
		if found, ok := findValue(v.Field(i), t); ok {
			return found, true
		}
	}
	return reflect.Value{}, false
}

// flagMapping maps each `flag` tag on T to its koanf dot-path.
func flagMapping[T any]() (map[string]string, error) {
	mapping := make(map[string]string)
	for _, f := range config.Fields[T]() {
		flag := f.Tag.Get("flag")
		if flag == "" {
			continue
		}
		if prev, dup := mapping[flag]; dup {
			return nil, fmt.Errorf("kongprovider: flag %q is tagged on both %s and %s", flag, prev, f.Path)
		}
		mapping[flag] = f.Path
	}
	return mapping, nil
}

// allFlags returns every flag of the CLI rooted at n, subcommands included.
func allFlags(n *kong.Node) []*kong.Flag {
	flags := slices.Clone(n.Flags)
	for _, child := range n.Children {
		flags = append(flags, allFlags(child)...)
	}
	return flags
}

// unknownFlags returns the mapping's flag names that no flag of the CLI
// rooted at n has, sorted.
func unknownFlags(n *kong.Node, mapping map[string]string) []string {
	flags := allFlags(n)
	var unknown []string
	for name := range mapping {
		if !slices.ContainsFunc(flags, func(fl *kong.Flag) bool { return fl.Name == name }) {
			unknown = append(unknown, "--"+name)
		}
	}
	slices.Sort(unknown)
	return unknown
}
