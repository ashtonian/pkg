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
//	kctx := kong.Parse(&cli, kongprovider.HelpFromConfig(&cli.AppConfig, "APP"))
//	cfg, err := config.Load[AppConfig](ctx,
//	    config.WithFile(cli.Config),
//	    config.WithEnvPrefix("APP"),
//	    kongprovider.WithOverridesFromStruct(kctx, &cli.AppConfig),
//	)
//
// For a CLI struct that does not hold the config struct, [WithOverrides]
// takes an explicit flag name → key mapping.
package kongprovider

import (
	"context"
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

// provider reads typed flags: through a name mapping (New), or as values
// copied when an option was built (WithOverridesFromStruct). It is a
// config.OriginProvider: a snapshot with provenance names the flag behind
// each value.
type provider struct {
	ctx     *kong.Context
	mapping map[string]string
	values  map[string]any
	flags   map[string]string // key → the flag that set it
	err     error
}

func (p provider) Read() (map[string]any, error) {
	values, _, err := p.read()
	if err != nil {
		return nil, err
	}
	return confmap.Provider(values, ".").Read()
}

// ReadWithOrigins returns what Read does, and the flag behind each key.
func (p provider) ReadWithOrigins(context.Context) (map[string]any, map[string]config.Source, error) {
	values, flags, err := p.read()
	if err != nil {
		return nil, nil, err
	}
	data, err := confmap.Provider(values, ".").Read()
	if err != nil {
		return nil, nil, err
	}
	origins := make(map[string]config.Source, len(flags))
	for key, flag := range flags {
		origins[key] = config.Source{Kind: config.SourceFlag, Name: "--" + flag}
	}
	return data, origins, nil
}

// read returns the typed values by key, and the flag that set each.
func (p provider) read() (values map[string]any, flags map[string]string, err error) {
	if p.err != nil {
		return nil, nil, p.err
	}
	if p.ctx == nil {
		return p.values, p.flags, nil
	}
	if unknown := unknownFlags(p.ctx.Model.Node, p.mapping); len(unknown) > 0 {
		return nil, nil, fmt.Errorf("kongprovider: the CLI has no flag named %s", strings.Join(unknown, ", "))
	}
	values, flags = make(map[string]any), make(map[string]string)
	for _, el := range p.ctx.Path {
		if el.Flag == nil || el.Resolved {
			continue
		}
		if key, ok := p.mapping[el.Flag.Name]; ok {
			values[key] = el.Flag.Target.Interface()
			flags[key] = el.Flag.Name
		}
	}
	return values, flags, nil
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

// WithOverridesFromStruct loads, as overrides, every flag typed on the
// command line that writes a field of cfg — the config struct the CLI struct
// embeds (`embed:""`), holds, or is:
//
//	var cli CLI // embeds AppConfig
//	kctx := kong.Parse(&cli, kongprovider.HelpFromConfig(&cli.AppConfig, "APP"))
//	cfg, err := config.Load[AppConfig](ctx, kongprovider.WithOverridesFromStruct(kctx, &cli.AppConfig))
//
// Each flag is mapped to the key of the field it writes, by the field's
// address (see config.KeyOf), so no tag has to name a flag and no flag name,
// prefix or alias has to agree with a key; flags outside cfg — the config
// file's path, say — are the CLI's own and are not forwarded. The typed
// values are copied when the option is built, so a reloader built from it
// keeps the command line it started with. A CLI none of whose flags writes
// into cfg fails the load with config.ErrSource.
func WithOverridesFromStruct[T any](ctx *kong.Context, cfg *T) config.Option {
	values, flags, err := typedValues(ctx, cfg)
	return config.WithOverrideProvider(provider{values: values, flags: flags, err: err}, nil)
}

// HelpFromConfig returns a Kong option that describes each flag that writes
// a field of cfg. A flag with no help of its own takes the field's `desc`
// tag, and its help gains the config key it sets and the environment
// variable that sets the same key under envPrefix. The field's default is
// added when diagnostics may print it (see config.Field.PrintableDefault) and
// Kong does not show it already; one that must be withheld is kept out of
// Kong's own placeholder too.
//
// Declare no Kong `required` on the config struct: Kong validates before the
// config pipeline runs, so a required flag could not be satisfied by a file,
// the environment or a secret. The config struct's `validate` rules, which
// run after every source, are the ones that apply.
func HelpFromConfig[T any](cfg *T, envPrefix string) kong.Option {
	return kong.PostBuild(func(k *kong.Kong) error {
		for _, fl := range allFlags(k.Model.Node) {
			f, ok, err := fieldOfFlag(cfg, fl)
			if err != nil {
				return err
			}
			if ok {
				describe(fl, f, envPrefix)
			}
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

// fieldOfFlag returns the field of cfg that flag fl writes; ok is false for a
// flag of the CLI's own.
func fieldOfFlag[T any](cfg *T, fl *kong.Flag) (f config.Field, ok bool, err error) {
	if !fl.Target.IsValid() || !fl.Target.CanAddr() {
		return config.Field{}, false, nil
	}
	key, err := config.KeyOfValue(cfg, fl.Target)
	switch {
	case errors.Is(err, config.ErrSchema):
		return config.Field{}, false, err
	case err != nil:
		return config.Field{}, false, nil
	}
	f, err = config.FieldFor[T](key)
	return f, err == nil, err
}

// typedValues copies the value of every flag typed on the command line that
// writes a field of cfg, keyed by that field's key, and names the flag.
func typedValues[T any](ctx *kong.Context, cfg *T) (map[string]any, map[string]string, error) {
	if cfg == nil {
		return nil, nil, errors.New("kongprovider: nil config")
	}
	writes := false
	for _, fl := range allFlags(ctx.Model.Node) {
		_, ok, err := fieldOfFlag(cfg, fl)
		if err != nil {
			return nil, nil, err
		}
		writes = writes || ok
	}
	if !writes {
		return nil, nil, fmt.Errorf("kongprovider: no flag of the CLI %s writes a field of %s; pass a pointer into the parsed CLI struct", ctx.Model.Name, reflect.TypeFor[T]())
	}
	values, flags := make(map[string]any), make(map[string]string)
	for _, el := range ctx.Path {
		if el.Flag == nil || el.Resolved {
			continue
		}
		f, ok, err := fieldOfFlag(cfg, el.Flag)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			values[f.Path] = copyValue(el.Flag.Target)
			flags[f.Path] = el.Flag.Name
		}
	}
	return values, flags, nil
}

// copyValue copies v so later changes to the flag's target cannot reach the
// copy: a slice or map gets storage of its own.
func copyValue(v reflect.Value) any {
	switch v.Kind() {
	case reflect.Slice:
		if v.IsNil() {
			return v.Interface()
		}
		c := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		reflect.Copy(c, v)
		return c.Interface()
	case reflect.Map:
		if v.IsNil() {
			return v.Interface()
		}
		c := reflect.MakeMapWithSize(v.Type(), v.Len())
		for iter := v.MapRange(); iter.Next(); {
			c.SetMapIndex(iter.Key(), iter.Value())
		}
		return c.Interface()
	}
	return v.Interface()
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
