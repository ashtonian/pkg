// Package kongprovider bridges Kong CLI flags to koanf config.
//
// It lives in a separate sub-package so that pkg/config does not depend
// on alecthomas/kong — only callers who import kongprovider take that
// dependency.
//
// Only flags explicitly provided by the user on the command line are
// forwarded. Kong default values are NOT included, to avoid overriding
// config file or environment variable values.
//
// # Usage
//
//	// Option A: derive mapping from flag:"" struct tags
//	cfg, err := config.GetConfig[AppConfig](
//	    config.WithEnvPrefix("APP"),
//	    kongprovider.WithOverridesFromTags[AppConfig](kongCtx),
//	)
//
//	// Option B: explicit mapping
//	cfg, err := config.GetConfig[AppConfig](
//	    config.WithEnvPrefix("APP"),
//	    kongprovider.WithOverrides(kongCtx, map[string]string{
//	        "host": "host",
//	        "port": "port",
//	    }),
//	)
package kongprovider

import (
	"reflect"
	"strings"

	"github.com/alecthomas/kong"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"

	"github.com/ashtonian/pkg/config"
)

// New creates a koanf.Provider from explicitly-set Kong flags.
// The mapping parameter maps Kong flag names to koanf dot-path keys.
// Only flags that were explicitly provided on the command line are
// included — Kong default values are NOT forwarded, to avoid
// overriding config file or env var values.
//
// Explicit detection scans ctx.Args for --name/--name=value patterns
// because kong sets Flag.Set=true even for applied defaults.
//
// The mapping values are koanf dot-paths (e.g. "listeners.tcp"). The
// returned provider unflattens them into the nested config tree via
// confmap.Provider so they merge under koanf's "." delimiter — exactly
// as config.GetConfig already does for its tag/programmatic defaults. A
// flat dotted-key map would insert a literal "listeners.tcp" top-level
// key that never overrides the nested node, so a nested CLI override
// like `--tcp :2000` would silently lose to the struct-tag default.
func New(ctx *kong.Context, mapping map[string]string) koanf.Provider {
	explicit := explicitFlags(ctx)
	values := make(map[string]any)
	for _, flag := range ctx.Flags() {
		if !explicit[flag.Name] {
			continue
		}
		koanfKey, ok := mapping[flag.Name]
		if !ok {
			continue
		}
		values[koanfKey] = flag.Target.Interface()
	}
	return confmap.Provider(values, ".")
}

// explicitFlags extracts flag names that were explicitly provided on the
// command line by scanning ctx.Args. This is necessary because kong sets
// Flag.Set=true for both user-provided and default-applied values.
func explicitFlags(ctx *kong.Context) map[string]bool {
	result := make(map[string]bool)
	// Build a reverse lookup from short rune to long name.
	shortToLong := make(map[rune]string)
	for _, f := range ctx.Flags() {
		if f.Short != 0 {
			shortToLong[f.Short] = f.Name
		}
	}
	for _, arg := range ctx.Args {
		if strings.HasPrefix(arg, "--") {
			name := strings.TrimPrefix(arg, "--")
			if idx := strings.Index(name, "="); idx >= 0 {
				name = name[:idx]
			}
			result[name] = true
		} else if strings.HasPrefix(arg, "-") && len(arg) >= 2 && arg[1] != '-' {
			// Short flags: -p, -abc (bundled).
			for _, r := range arg[1:] {
				if longName, ok := shortToLong[r]; ok {
					result[longName] = true
				}
			}
		}
	}
	return result
}

// WithOverrides returns a config.Option that adds a kongprovider as
// an extra provider with explicit flag-to-koanf-key mapping.
func WithOverrides(ctx *kong.Context, mapping map[string]string) config.Option {
	return config.WithProvider(New(ctx, mapping))
}

// WithOverridesFromTags derives the flag-to-koanf-key mapping
// automatically from `flag:"flagname"` struct tags on the config
// struct T. It walks the struct recursively, building a mapping from
// the flag tag value to the koanf dot-path (constructed from koanf
// tags). Returns a config.Option.
func WithOverridesFromTags[T any](ctx *kong.Context) config.Option {
	mapping := buildFlagMapping[T]()
	return WithOverrides(ctx, mapping)
}

// buildFlagMapping walks the struct type T and builds a map from
// flag tag values to koanf dot-paths.
func buildFlagMapping[T any]() map[string]string {
	mapping := make(map[string]string)
	var zero T
	t := reflect.TypeOf(zero)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return mapping
	}
	walkFlagTags(t, "", mapping)
	return mapping
}

// walkFlagTags recursively walks struct fields, building the flag→koanf mapping.
func walkFlagTags(t reflect.Type, prefix string, out map[string]string) {
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		koanfTag := field.Tag.Get("koanf")
		if koanfTag == "-" {
			continue
		}
		if koanfTag == "" {
			koanfTag = strings.ToLower(field.Name)
		}

		key := koanfTag
		if prefix != "" {
			key = prefix + "." + koanfTag
		}

		ft := field.Type
		if ft.Kind() == reflect.Ptr {
			ft = ft.Elem()
		}

		// Recurse into nested structs (but not special types like time.Duration).
		if ft.Kind() == reflect.Struct && ft.PkgPath() != "time" {
			walkFlagTags(ft, key, out)
			continue
		}

		// If this field has a flag tag, add the mapping.
		if flagTag := field.Tag.Get("flag"); flagTag != "" {
			out[flagTag] = key
		}
	}
}
