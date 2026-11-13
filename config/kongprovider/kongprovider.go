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
// The default wiring embeds the config struct in the command that uses it:
// Kong makes a flag of each field, and [Load] loads the config during Parse,
// before Kong validates, and writes it into that struct. The command's
// fields, a Run method's *AppConfig parameter, and the Validate methods Kong
// calls all see the loaded config: files, environment, secrets and typed
// flags.
//
//	type AppConfig struct {
//	    Port  int  `koanf:"port" default:"8080" short:"p" desc:"HTTP listen port"`
//	    Debug bool `koanf:"debug" negatable:""`
//	}
//	type ServeCmd struct {
//	    AppConfig `embed:""`
//	}
//	func (c *ServeCmd) Run() error { return serve(c.Port) }
//
//	type CLI struct {
//	    Config string   `type:"path" default:"config.yaml" help:"Config file."`
//	    Serve  ServeCmd `cmd:""`
//	}
//
//	var cli CLI
//	kctx := kong.Parse(&cli, kongprovider.Load(ctx, &cli.Serve.AppConfig, []string{"APP"},
//	    func(*kong.Context) ([]config.Option, error) {
//	        return []config.Option{config.WithFile(cli.Config)}, nil
//	    }))
//	kctx.FatalIfErrorf(kctx.Run())
//
// A service that reloads uses [LoadReloader], which binds a
// config.Reloader[T] for Run instead.
//
// [HelpFromConfig] and [WithOverridesFromStruct] are the two halves of that
// wiring, for a program that loads the config itself after Parse. Kong then
// validates and exposes its own copy of the struct — defaults and typed
// flags only — so a Validate method on the config is called on that copy
// before any file or variable is read. For a CLI struct that does not hold
// the config struct, [WithOverrides] takes an explicit flag name → key
// mapping.
package kongprovider

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
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
// That is [Load]'s wiring done by hand, after Parse: cli.AppConfig keeps
// Kong's defaults and typed flags, and Kong has already called its Validate
// methods on them. Prefer Load unless the program needs the load outside
// Parse.
//
// Each flag is mapped to the key of the field it writes, by the field's
// address (see config.MatchFields), so no tag has to name a flag and no flag name,
// prefix or alias has to agree with a key; flags outside cfg — the config
// file's path, say — are the CLI's own and are not forwarded. The typed
// values are copied when the option is built, so a reloader built from it
// keeps the command line it started with. A CLI none of whose flags writes
// into cfg fails the load with config.ErrSource.
// Invalid config field catalogs fail the load with config.ErrSchema, including
// when no CLI flag maps to the conflicting fields. Ambiguous field addresses
// fail with config.ErrKey. Both errors also match config.ErrSource during loading.
func WithOverridesFromStruct[T any](ctx *kong.Context, cfg *T) config.Option {
	values, flags, err := typedValues(ctx, cfg)
	return config.WithOverrideProvider(provider{values: values, flags: flags, err: err}, nil)
}

// HelpFromConfig returns a Kong option that describes each flag that writes
// a field of cfg. A flag with no help of its own takes the field's `desc`
// tag, and its help gains the config key it sets and the environment
// variables that set the same key under envPrefixes, as
// config.WithEnvPrefixes takes them, in the order the load reads them: the
// key's variable under each prefix, most specific first, then those its
// `env` tag lists. A load with config.WithEnvPrefixes("APP_WORKER", "APP")
// passes HelpFromConfig(cfg, "APP_WORKER", "APP"). The key's default — its
// tag's, or what a config.Defaulter sets at that path, as config.Usage
// shows it — is added when Kong shows none, and replaces Kong's placeholder
// when a Defaulter sets another than the tag Kong shows; one that must be
// withheld is kept out of Kong's placeholder too.
//
// Kong does not read those flags' `env` tags: the config package reads the
// variables in its environment layer, where they have their precedence and
// a value that does not decode is reported without being quoted. Kong would
// parse them as flag defaults before the config loads, failing — and quoting
// the value — on a variable meant for something else, such as a Kubernetes
// service link (REDIS_PORT=tcp://10.0.0.1:6379).
//
// Declare no Kong `required` on the config struct: Kong checks it against
// the command line alone, so a required flag could not be satisfied by a
// file, the environment or a secret. The config struct's `validate` rules,
// which run after every source, are the ones that apply.
// An invalid config field catalog returns config.ErrSchema during parser
// construction, even when the conflicting fields have no CLI flags.
// Ambiguous field addresses return config.ErrKey. [Load] and [LoadReloader]
// describe the flags themselves.
func HelpFromConfig[T any](cfg *T, envPrefixes ...string) kong.Option {
	envPrefixes = slices.Clone(envPrefixes)
	return kong.PostBuild(func(k *kong.Kong) error {
		_, err := describeFlags(k, cfg, envPrefixes)
		return err
	})
}

// describeFlags describes every flag of the CLI k built that writes a field
// of cfg, and returns those flags with their fields.
func describeFlags[T any](k *kong.Kong, cfg *T, envPrefixes []string) (map[*kong.Flag]config.Field, error) {
	flags := allFlags(k.Model.Node)
	fields, err := fieldsOfFlags(cfg, flags)
	if err != nil {
		return nil, err
	}
	defaults, err := defaultsOf[T](envPrefixes)
	if err != nil {
		return nil, err
	}
	for _, fl := range flags {
		if f, ok := fields[fl]; ok {
			describe(fl, f, defaults[f.Path], envPrefixes)
		}
	}
	return fields, nil
}

// flagDefault is a key's default as diagnostics may show it: its text, or
// withheld.
type flagDefault struct {
	text     string
	withheld bool
}

// defaultsOf is the default of every key of T, by key, from its usage
// catalog: a tag's as written, or what a Defaulter sets at that path.
func defaultsOf[T any](envPrefixes []string) (map[string]flagDefault, error) {
	rows, err := config.Usage[T](envPrefixes...)
	if err != nil {
		return nil, err
	}
	out := make(map[string]flagDefault, len(rows))
	for _, r := range rows {
		out[r.Key] = flagDefault{text: r.Default, withheld: r.Withheld}
	}
	return out, nil
}

// describe writes field f's description into flag fl's help, and its
// default where Kong would show another or none.
func describe(fl *kong.Flag, f config.Field, def flagDefault, envPrefixes []string) {
	if fl.Help == "" {
		fl.Help = f.Tag.Get("desc")
	}
	fl.Tag.Envs, fl.Envs = nil, nil
	hints := []string{"config: " + f.Path}
	if envs := f.EnvVars(envPrefixes...); len(envs) > 0 {
		hints = append(hints, "env: "+strings.Join(envs, ", "))
	}
	switch {
	case def.withheld:
		if fl.HasDefault {
			fl.PlaceHolder = "VALUE" // Kong would print the default as the placeholder
		}
	case !fl.HasDefault:
		if def.text != "" {
			hints = append(hints, "default: "+def.text)
		}
	case fl.Default != def.text:
		// A Defaulter sets another default at this path than the tag Kong shows.
		fl.PlaceHolder = def.text
		if fl.Target.Kind() == reflect.String {
			fl.PlaceHolder = strconv.Quote(def.text)
		}
	}
	fl.Help = strings.TrimSpace(fl.Help + " (" + strings.Join(hints, "; ") + ")")
}

// fieldsOfFlags associates all targets in one traversal of this config instance.
func fieldsOfFlags[T any](cfg *T, flags []*kong.Flag) (map[*kong.Flag]config.Field, error) {
	targets := make([]reflect.Value, len(flags))
	for i, fl := range flags {
		targets[i] = fl.Target
	}
	matches, err := config.MatchFields(cfg, targets)
	if err != nil {
		return nil, err
	}
	fields := make(map[*kong.Flag]config.Field, len(matches))
	for i, match := range matches {
		if match.Path != "" {
			fields[flags[i]] = match
		}
	}
	return fields, nil
}

// typedValues copies the value of every flag typed on the command line that
// writes a field of cfg, keyed by that field's key, and names the flag.
func typedValues[T any](ctx *kong.Context, cfg *T) (map[string]any, map[string]string, error) {
	if cfg == nil {
		return nil, nil, errors.New("kongprovider: nil config")
	}
	fields, err := fieldsOfFlags(cfg, allFlags(ctx.Model.Node))
	if err != nil {
		return nil, nil, err
	}
	if len(fields) == 0 {
		return nil, nil, errNoFlag(ctx.Model.Name)
	}
	values, flags := make(map[string]any), make(map[string]string)
	for _, el := range ctx.Path {
		if el.Flag == nil || el.Resolved {
			continue
		}
		f, ok := fields[el.Flag]
		if ok {
			values[f.Path] = copyValue(el.Flag.Target)
			flags[f.Path] = el.Flag.Name
		}
	}
	return values, flags, nil
}

// copyValue copies v, a flag's target, so later changes to the target — or
// to anything it points at — cannot reach the copy: every slice, map and
// pointer gets storage of its own, shared storage and cycles kept as they
// were.
func copyValue(v reflect.Value) any {
	c := copier{made: map[copied]reflect.Value{}}
	return c.copy(v).Interface()
}

type copier struct {
	made map[copied]reflect.Value
}

// copied identifies a slice, map or pointer already copied; a slice's length
// is part of it, two slices over one array being different values.
type copied struct {
	ptr uintptr
	len int
	typ reflect.Type
}

func (c copier) copy(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if v.IsNil() {
			return v
		}
		key := copied{ptr: v.Pointer(), typ: v.Type()}
		if v.Kind() == reflect.Slice {
			key.len = v.Len()
		}
		if done, ok := c.made[key]; ok {
			return done
		}
		switch v.Kind() {
		case reflect.Pointer:
			p := reflect.New(v.Type().Elem())
			c.made[key] = p
			p.Elem().Set(c.copy(v.Elem()))
			return p
		case reflect.Map:
			m := reflect.MakeMapWithSize(v.Type(), v.Len())
			c.made[key] = m
			for it := v.MapRange(); it.Next(); {
				m.SetMapIndex(c.copy(it.Key()), c.copy(it.Value()))
			}
			return m
		default:
			s := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
			c.made[key] = s
			for i := range v.Len() {
				s.Index(i).Set(c.copy(v.Index(i)))
			}
			return s
		}
	case reflect.Array:
		a := reflect.New(v.Type()).Elem()
		for i := range v.Len() {
			a.Index(i).Set(c.copy(v.Index(i)))
		}
		return a
	case reflect.Struct:
		s := reflect.New(v.Type()).Elem()
		s.Set(v) // unexported fields cannot be reached, so they keep their values
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				s.Field(i).Set(c.copy(v.Field(i)))
			}
		}
		return s
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		i := reflect.New(v.Type()).Elem()
		i.Set(c.copy(v.Elem()))
		return i
	}
	return v
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
