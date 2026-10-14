package config

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"reflect"
	"slices"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/v2"
)

// Option configures [Load] and the pipeline a [Reloader] re-runs.
type Option func(*options)

// SecretPopulator fills `secret`-tagged fields on the target struct, a
// pointer to the freshly decoded config. It is a function, not an interface,
// so a caller wires any secret backend without this package importing it.
type SecretPopulator func(ctx context.Context, target any) error

// Validator validates the decoded config. A go-playground *validator.Validate
// satisfies it, as does any wrapper with a Struct method. A validator that
// also has StructCtx(ctx, v) error is called through that instead, with the
// load's ctx.
type Validator interface {
	Struct(v any) error
}

type options struct {
	envPrefix    string
	environ      func() []string
	files        []fileSource
	fileParser   koanf.Parser
	defaults     map[string]any
	base         []providerSource
	overrides    []providerSource
	decodeHooks  []mapstructure.DecodeHookFunc
	allowUnknown bool
	secrets      SecretPopulator
	postLoad     []postLoadHook
	skipValid    bool
	validator    Validator
	logger       *slog.Logger
}

type fileSource struct {
	path     string
	required bool
}

type providerSource struct {
	provider koanf.Provider
	parser   koanf.Parser
}

// postLoadHook is a [WithPostLoad] callback with its config type erased, so
// Option stays non-generic; target is the *T it was written for, checked
// before a load reads anything.
type postLoadHook struct {
	target reflect.Type
	run    func(ctx context.Context, cfg any) error
}

func buildOptions(opts []Option) *options {
	o := &options{}
	for _, opt := range opts {
		opt(o)
	}
	if o.environ == nil {
		o.environ = os.Environ
	}
	if o.fileParser == nil {
		o.fileParser = yaml.Parser()
	}
	return o
}

// requireFiles marks every configured file that loaded as required. A
// Reloader uses it so a file that loaded once cannot silently drop out of a
// reload.
func (o *options) requireFiles(loaded fileDigests) {
	for i := range o.files {
		if _, ok := loaded[o.files[i].path]; ok {
			o.files[i].required = true
		}
	}
}

func (o *options) filePaths() []string {
	paths := make([]string, 0, len(o.files))
	for _, f := range o.files {
		paths = append(paths, f.path)
	}
	return paths
}

// WithFile adds an optional config file. A missing file is skipped; one that
// exists but cannot be read fails the load with [ErrFileRead], and one the
// file parser rejects fails it with [ErrFileParse]. Files merge in the order
// added, a later file winning on a shared key.
//
// Under a [Reloader], a file that loaded once is required from then on: a
// reload that finds it gone fails and keeps the previous config rather than
// quietly reverting its keys to lower-precedence values.
func WithFile(path string) Option {
	return func(o *options) {
		o.files = append(o.files, fileSource{path: path})
	}
}

// WithFiles adds several optional config files, merged in order. Each
// follows the [WithFile] rules.
func WithFiles(paths ...string) Option {
	return func(o *options) {
		for _, p := range paths {
			o.files = append(o.files, fileSource{path: p})
		}
	}
}

// WithRequiredFile adds a config file that must exist: a missing or
// unreadable file fails the load with [ErrFileRead], one the file parser
// rejects with [ErrFileParse].
func WithRequiredFile(path string) Option {
	return func(o *options) {
		o.files = append(o.files, fileSource{path: path, required: true})
	}
}

// WithRequiredFiles adds several required config files, merged in order.
func WithRequiredFiles(paths ...string) Option {
	return func(o *options) {
		for _, p := range paths {
			o.files = append(o.files, fileSource{path: p, required: true})
		}
	}
}

// WithFileParser sets the parser for every config file: any koanf parser,
// e.g. toml.Parser() or dotenv.Parser(). The default is YAML, which also
// reads JSON.
func WithFileParser(p koanf.Parser) Option {
	return func(o *options) {
		o.fileParser = p
	}
}

// WithEnvPrefix enables environment variables named PREFIX_<KEY>, the key
// path upper-cased with "." → "_" (APP_DB_MAX_CONNS sets db.max_conns). The
// environment is not read without a prefix.
func WithEnvPrefix(prefix string) Option {
	return func(o *options) {
		o.envPrefix = prefix
	}
}

// WithEnviron sets where environment variables are read from: a function
// returning "NAME=value" entries, as [os.Environ] does, called on every load
// and reload. The default is os.Environ. Tests and examples pass a fixed list;
// a deployment adapter can pass variables gathered elsewhere.
func WithEnviron(environ func() []string) Option {
	return func(o *options) {
		o.environ = environ
	}
}

// WithDefaults adds programmatic default values keyed by koanf dot-path
// (e.g. "server.port"); they override `default` tags. Repeated calls merge,
// a later call winning on a shared key. The values are deep-copied when
// WithDefaults is called — every map key and value, slice, array and pointer
// target reachable through exported fields — so changing the caller's data
// afterwards changes nothing, including what a Reloader built from the
// option reads later. Unexported struct fields, channels and functions are
// copied by value.
func WithDefaults(defaults map[string]any) Option {
	snapshot := deepCopy(reflect.ValueOf(defaults)).Interface().(map[string]any)
	return func(o *options) {
		if o.defaults == nil {
			o.defaults = make(map[string]any, len(snapshot))
		}
		maps.Copy(o.defaults, snapshot)
	}
}

// WithBaseProvider adds a koanf provider loaded above the defaults and below
// the config files — the place for a remote store (Consul, etcd, S3) that
// local files and the environment refine. parser is nil for a provider that
// returns a map (Read), or the parser for one that returns bytes
// (ReadBytes): a file, an HTTP body. Base providers load in the order added.
//
// A provider that implements [ContextProvider] is read with the load's
// context. A provider failing to load fails the load with [ErrSource].
func WithBaseProvider(p koanf.Provider, parser koanf.Parser) Option {
	return func(o *options) {
		o.base = append(o.base, providerSource{provider: p, parser: parser})
	}
}

// WithOverrideProvider adds a koanf provider loaded above the environment,
// at the highest precedence — the place for explicit CLI flags (see the
// kongprovider package) and other operator overrides. Override providers
// load in the order added, a later one winning on a shared key. parser,
// [ContextProvider] and failures follow [WithBaseProvider].
func WithOverrideProvider(p koanf.Provider, parser koanf.Parser) Option {
	return func(o *options) {
		o.overrides = append(o.overrides, providerSource{provider: p, parser: parser})
	}
}

// WithDecodeHook adds a mapstructure decode hook. Hooks run in the order
// added, before the built-in ones (string → time.Duration, comma-separated
// string → slice, encoding.TextUnmarshaler), so a hook can take over a
// conversion the built-ins would otherwise make — splitting a slice on ";",
// say, or decoding a custom type from a map.
func WithDecodeHook(hook mapstructure.DecodeHookFunc) Option {
	return func(o *options) {
		o.decodeHooks = append(o.decodeHooks, hook)
	}
}

// WithAllowUnknownKeys lets a load succeed when a config file, provider or
// WithDefaults sets a key no field decodes. Without it — the default — such
// a key, a typo or a setting that was removed, fails the load with
// [ErrValidation] naming each one (server.prot, items[0].prot,
// named["primary"].prot), so a misspelled key cannot leave a service running
// on the default it was meant to override. The decoder reports the keys it
// did not use while it decodes the merged sources, so the check follows
// decoding exactly: into slice elements and map entries of structs, through
// squashed structs, and past input a decode hook or a self-decoding type
// consumes. A map's own keys, anything beneath an interface, and keys a
// koanf:",remain" field collects are accepted. The merged tree does not record
// which source set a key, so the error names keys only. An environment
// variable that fits no key is never loaded, so it cannot fail a load; it is
// logged at debug level.
func WithAllowUnknownKeys() Option {
	return func(o *options) {
		o.allowUnknown = true
	}
}

// WithSecretPopulator sets the secret resolver. It runs after decoding with
// the load's context and a pointer to the decoded config; its error fails the
// load with [ErrSecretProvider].
func WithSecretPopulator(p SecretPopulator) Option {
	return func(o *options) {
		o.secrets = p
	}
}

// WithPostLoad registers a hook that runs after decoding and secret
// resolution, and before validation: the place for derivation and
// normalization decode hooks cannot express — collapsing a shorthand into its
// canonical form, expanding a profile into the knobs it stands for, deriving
// a default from the host.
//
// Because the hook runs inside the load, a [Reloader] applies it on every
// reload before deciding whether to swap, so normalization is covered by
// validate-before-swap. The same work done after Load returns is not: under a
// Reloader the new config is already live by the time it fails.
//
// The hook mutates *cfg in place. Its error fails the load with
// [ErrPostLoad] and, under a Reloader, keeps the previous config. Hooks run
// in registration order; the first error stops the chain. A hook written for
// a type other than the one loaded fails the load before any source is read.
func WithPostLoad[T any](fn func(ctx context.Context, cfg *T) error) Option {
	hook := postLoadHook{
		target: reflect.TypeFor[*T](),
		run:    func(ctx context.Context, cfg any) error { return fn(ctx, cfg.(*T)) },
	}
	return func(o *options) {
		o.postLoad = append(o.postLoad, hook)
	}
}

// WithValidator sets the validator. nil restores the default: a
// go-playground validator that names fields by config key. Register
// [FieldName] on a custom go-playground validator to get the same naming.
func WithValidator(v Validator) Option {
	return func(o *options) {
		o.validator = v
	}
}

// WithSkipValidation disables validation — the validator, validate:"required"
// included, and a root Validate method (see [Validatable]). Unknown keys still
// fail the load; [WithAllowUnknownKeys] is the separate escape hatch for those.
func WithSkipValidation() Option {
	return func(o *options) {
		o.skipValid = true
	}
}

// WithLogger sets the logger for load diagnostics (the sources loaded, and
// environment variables that fit no key, at debug level) and, for a
// [Reloader] built by [NewReloader], its reload events. Without it, or with a
// nil logger, every line goes to slog.Default() as it is when the line is
// written, so a logger configured after construction — from the config just
// loaded, say — takes effect.
func WithLogger(logger *slog.Logger) Option {
	return func(o *options) {
		o.logger = logger
	}
}

// log is the logger for one line: WithLogger's, or slog.Default() now.
func (o *options) log() *slog.Logger {
	if o.logger != nil {
		return o.logger
	}
	return slog.Default()
}

// decodeHook composes the caller's decode hooks, first, with the built-in
// ones: string → time.Duration, comma-separated string → slice (koanf's
// defaults do not split env values and tag defaults), TextUnmarshaler.
func (o *options) decodeHook() mapstructure.DecodeHookFunc {
	hooks := append(slices.Clone(o.decodeHooks),
		mapstructure.StringToTimeDurationHookFunc(),
		mapstructure.StringToSliceHookFunc(","),
		mapstructure.TextUnmarshallerHookFunc(),
	)
	return mapstructure.ComposeDecodeHookFunc(hooks...)
}

// checkHookTypes reports a post-load hook written for a type other than cfg.
func (o *options) checkHookTypes(cfg reflect.Type) error {
	for i, h := range o.postLoad {
		if h.target != cfg {
			return fmt.Errorf("%w: hook %d is for %s, the load produces %s", ErrPostLoad, i, h.target, cfg)
		}
	}
	return nil
}
