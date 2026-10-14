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
//	type CLI struct {
//	    Port  int  `help:"Port to listen on."`
//	    Debug bool `negatable:""`
//	}
//	type AppConfig struct {
//	    Port  int  `koanf:"port" default:"8080" flag:"port" desc:"HTTP listen port"`
//	    Debug bool `koanf:"debug" flag:"debug"`
//	}
//
//	kctx := kong.Parse(&cli, kongprovider.HelpFromTags[AppConfig]("APP"))
//	cfg, err := config.Load[AppConfig](ctx,
//	    config.WithFile("config.yaml"),
//	    config.WithEnvPrefix("APP"),
//	    kongprovider.WithOverridesFromTags[AppConfig](kctx),
//	)
package kongprovider

import (
	"errors"
	"fmt"
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
// same keys defaults and environment variables set. Two fields tagged with
// one flag name fail the load.
func WithOverridesFromTags[T any](ctx *kong.Context) config.Option {
	mapping, err := flagMapping[T]()
	return config.WithOverrideProvider(provider{ctx: ctx, mapping: mapping, err: err}, nil)
}

// HelpFromTags returns a Kong option that describes each flag a `flag` tag
// on T names: a flag with no help of its own takes the field's `desc` tag,
// and every such flag's help gains the config key it sets, the environment
// variable that sets the same key under envPrefix, and the field's default
// when diagnostics may print it (see config.Field.PrintableDefault). Kong fails to build the CLI when a `flag`
// tag names no flag, or when two fields name one flag.
//
// Declare no defaults and no `required` on the CLI's own flags: a flag the
// user did not type is not forwarded, so the config struct's `default` tags
// and its validation, which run after files, the environment and secrets,
// are the ones that apply.
func HelpFromTags[T any](envPrefix string) kong.Option {
	return kong.PostBuild(func(k *kong.Kong) error {
		byFlag := make(map[string]config.Field)
		for _, f := range config.Fields[T]() {
			if name := f.Tag.Get("flag"); name != "" {
				if prev, dup := byFlag[name]; dup {
					return fmt.Errorf("kongprovider: flag %q is tagged on both %s and %s", name, prev.Path, f.Path)
				}
				byFlag[name] = f
			}
		}
		flags := allFlags(k.Model.Node)
		for name, f := range byFlag {
			if !slices.ContainsFunc(flags, func(fl *kong.Flag) bool { return fl.Name == name }) {
				return fmt.Errorf("kongprovider: %s is tagged flag %q, but the CLI has no such flag", f.Path, name)
			}
		}
		for _, fl := range flags {
			f, ok := byFlag[fl.Name]
			if !ok {
				continue
			}
			if fl.Help == "" {
				fl.Help = f.Tag.Get("desc")
			}
			hints := []string{"config: " + f.Path}
			if env := f.EnvVar(envPrefix); env != "" {
				hints = append(hints, "env: "+env)
			}
			if def, ok := f.PrintableDefault(); ok {
				hints = append(hints, "default: "+def)
			}
			fl.Help = strings.TrimSpace(fl.Help + " (" + strings.Join(hints, "; ") + ")")
		}
		return nil
	})
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
