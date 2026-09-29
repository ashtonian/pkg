// Package kongprovider bridges Kong CLI flags to koanf config.
//
// It lives in a separate sub-package so that config does not
// depend on alecthomas/kong — only callers who import kongprovider take that
// dependency.
//
// Only flags the user typed on the command line are forwarded, in any
// spelling Kong accepts: long, short, alias, `--flag=value`, and negated
// booleans (`--no-flag`). Kong defaults and values Kong resolved from its own
// resolvers are NOT forwarded, so they never override config file or
// environment values.
//
// # Usage
//
//	// Option A: derive mapping from flag:"" struct tags
//	cfg, err := config.GetConfig[AppConfig](ctx,
//	    config.WithEnvPrefix("APP"),
//	    kongprovider.WithOverridesFromTags[AppConfig](kongCtx),
//	)
//
//	// Option B: explicit mapping
//	cfg, err := config.GetConfig[AppConfig](ctx,
//	    config.WithEnvPrefix("APP"),
//	    kongprovider.WithOverrides(kongCtx, map[string]string{
//	        "host": "host",
//	        "port": "port",
//	    }),
//	)
package kongprovider

import (
	"github.com/alecthomas/kong"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"

	"github.com/ashtonian/pkg/config"
)

// New creates a koanf.Provider from the Kong flags the user typed. The
// mapping parameter maps Kong flag names to koanf dot-path keys.
//
// Kong records each flag it parses from argv as an element of ctx.Path, under
// the flag's canonical name whatever spelling was typed, with the value
// already applied — so `--no-feature` arrives as feature=false. Elements a
// resolver supplied are marked Resolved, and defaults never appear, which is
// exactly the "explicitly set" distinction this provider needs. Flag.Set
// cannot be used for it: Kong sets that for applied defaults too.
//
// The mapping values are koanf dot-paths (e.g. "listeners.tcp"). The
// returned provider unflattens them into the nested config tree via
// confmap.Provider so they merge under koanf's "." delimiter — exactly
// as config.GetConfig already does for its tag/programmatic defaults. A
// flat dotted-key map would insert a literal "listeners.tcp" top-level
// key that never overrides the nested node, so a nested CLI override
// like `--tcp :2000` would silently lose to the struct-tag default.
func New(ctx *kong.Context, mapping map[string]string) koanf.Provider {
	values := make(map[string]any)
	for _, p := range ctx.Path {
		if p.Flag == nil || p.Resolved {
			continue
		}
		if koanfKey, ok := mapping[p.Flag.Name]; ok {
			values[koanfKey] = p.Flag.Target.Interface()
		}
	}
	return confmap.Provider(values, ".")
}

// WithOverrides returns a config.Option that adds a kongprovider as
// an extra provider with explicit flag-to-koanf-key mapping.
func WithOverrides(ctx *kong.Context, mapping map[string]string) config.Option {
	return config.WithProvider(New(ctx, mapping))
}

// WithOverridesFromTags derives the flag-to-koanf-key mapping from
// `flag:"flagname"` struct tags on the config struct T, using the key paths
// [config.Fields] reports — the same ones defaults and environment variables
// resolve to.
func WithOverridesFromTags[T any](ctx *kong.Context) config.Option {
	return WithOverrides(ctx, flagMapping[T]())
}

// flagMapping maps each `flag` tag on T to its koanf dot-path.
func flagMapping[T any]() map[string]string {
	mapping := make(map[string]string)
	for _, f := range config.Fields[T]() {
		if flag := f.Tag.Get("flag"); flag != "" {
			mapping[flag] = f.Path
		}
	}
	return mapping
}
