package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/go-playground/validator/v10"
	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	kenv "github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// SecretPopulator fills `secret`-tagged fields on the target struct.
//
// Implementations typically wrap a secret store's client. Keeping this as
// a function-typed option rather than an interface keeps the package free
// of any secret backend while letting consumers wire any secret backend
// they want.
type SecretPopulator func(ctx context.Context, target any) error

// Option configures GetConfig behavior.
type Option func(*options)

// Validator is the narrow interface pkgconfig consumes for struct
// validation. Both `*validator.Validate` (go-playground) and any
// custom wrapper (e.g. a ModelValidator)
// satisfy it. A validator that also has
// `StructCtx(ctx, v) error` is called through that instead, with the
// load's ctx.
type Validator interface {
	Struct(v any) error
}

type options struct {
	envPrefix       string
	configFiles     []configFileEntry
	defaults        map[string]any
	populateSecrets SecretPopulator
	postLoad        []postLoadHook
	skipValidation  bool
	customValidator Validator
	extraProviders  []koanf.Provider
	logger          *slog.Logger
}

// postLoadHook is the type-erased form of a [WithPostLoad] callback. The
// generic constructor closes over the *T assertion so call sites stay typed
// while the Option list stays non-generic.
type postLoadHook func(ctx context.Context, target any) error

type configFileEntry struct {
	path     string
	required bool
}

func buildOptions(opts []Option) *options {
	o := &options{}
	for _, opt := range opts {
		opt(o)
	}
	if o.logger == nil {
		o.logger = slog.Default()
	}
	return o
}

// requireFiles marks every configured file in paths as required. A Reloader
// uses it so a file that loaded once cannot silently drop out of a reload.
func (o *options) requireFiles(paths []string) {
	for i := range o.configFiles {
		if slices.Contains(paths, o.configFiles[i].path) {
			o.configFiles[i].required = true
		}
	}
}

// WithEnvPrefix sets the prefix for environment variable lookups.
// For example, WithEnvPrefix("APP") causes env var APP_HOST to map to koanf key "host".
func WithEnvPrefix(prefix string) Option {
	return func(o *options) {
		o.envPrefix = prefix
	}
}

// WithConfigFile adds an optional YAML config file path. Optional means the
// file may be absent: a missing file is skipped. A file that exists but cannot
// be read fails the load with [ErrFileRead], and one that is not valid YAML
// fails it with [ErrFileParse].
//
// Under a [Reloader], a file that loaded once is required from then on: a
// reload that finds it gone fails and keeps the previous config rather than
// quietly reverting its keys to lower-precedence values.
func WithConfigFile(path string) Option {
	return func(o *options) {
		o.configFiles = append(o.configFiles, configFileEntry{path: path, required: false})
	}
}

// WithMustConfigFile adds a required config file path. A missing or
// unreadable file fails the load with [ErrFileRead]; invalid YAML fails it
// with [ErrFileParse].
func WithMustConfigFile(path string) Option {
	return func(o *options) {
		o.configFiles = append(o.configFiles, configFileEntry{path: path, required: true})
	}
}

// WithConfigFiles adds multiple optional config file paths, merged
// left-to-right. Each follows the [WithConfigFile] rules.
func WithConfigFiles(paths ...string) Option {
	return func(o *options) {
		for _, p := range paths {
			o.configFiles = append(o.configFiles, configFileEntry{path: p, required: false})
		}
	}
}

// WithMustConfigFiles adds multiple required config file paths, merged left-to-right.
func WithMustConfigFiles(paths ...string) Option {
	return func(o *options) {
		for _, p := range paths {
			o.configFiles = append(o.configFiles, configFileEntry{path: p, required: true})
		}
	}
}

// WithDefaults adds programmatic default values keyed by koanf dot-path
// (e.g. "server.port"). Repeated calls merge, a later call winning on a
// shared key; the caller's map is copied, not retained.
func WithDefaults(defaults map[string]any) Option {
	return func(o *options) {
		if o.defaults == nil {
			o.defaults = make(map[string]any, len(defaults))
		}
		maps.Copy(o.defaults, defaults)
	}
}

// WithProvider adds a generic koanf provider loaded at high precedence
// (after env vars). Multiple providers are loaded in the order added.
// This is the extension point for CLI flag overrides (via kongprovider),
// remote config sources, or any custom koanf.Provider.
func WithProvider(p koanf.Provider) Option {
	return func(o *options) {
		o.extraProviders = append(o.extraProviders, p)
	}
}

// WithSecretPopulator configures secret injection.
//
// After unmarshaling, GetConfig invokes the populator with the same ctx
// supplied to GetConfig and a pointer to the freshly-loaded config struct.
// The populator is responsible for walking `secret:"..."` tags and
// resolving values from the consumer's chosen secret backend
// (typically a secret store client's populate call).
func WithSecretPopulator(p SecretPopulator) Option {
	return func(o *options) {
		o.populateSecrets = p
	}
}

// WithPostLoad registers a hook that runs after the config is unmarshaled
// and secrets are injected, but BEFORE validation. Use it for derivation and
// normalization the mapstructure decode hooks cannot express: collapsing a
// shorthand block into its canonical form, expanding a profile macro into the
// knobs it stands for, parsing a delimited field into a typed slice, deriving
// a default from the host environment.
//
// Ordering is the point of the hook. Because it runs inside GetConfig, a
// [Reloader] applies it on every reload before deciding whether to swap — so
// normalization is covered by validate-before-swap like everything else.
// Equivalent work done after GetConfig returns is not: under a Reloader the
// new config is already live by the time such a step fails.
//
// The hook mutates *cfg in place. A returned error aborts the load — and under
// a Reloader preserves the previous config — wrapped in [ErrPostLoad].
// Multiple hooks run in registration order; the first error stops the chain.
//
//	cfg, err := config.GetConfig[AppConfig](ctx,
//	    config.WithConfigFile("./config.yaml"),
//	    config.WithPostLoad(func(_ context.Context, c *AppConfig) error {
//	        c.Normalize()
//	        return c.ValidateCrossField()
//	    }),
//	)
func WithPostLoad[T any](fn func(ctx context.Context, cfg *T) error) Option {
	return func(o *options) {
		o.postLoad = append(o.postLoad, func(ctx context.Context, target any) error {
			typed, ok := target.(*T)
			if !ok {
				return fmt.Errorf("%w: hook is typed for %T but the load produced %T",
					ErrPostLoad, (*T)(nil), target)
			}
			return fn(ctx, typed)
		})
	}
}

// WithSkipValidation disables go-playground/validator validation
// (including validate:"required" checks).
func WithSkipValidation() Option {
	return func(o *options) {
		o.skipValidation = true
	}
}

// WithValidator sets a custom struct validator. Accepts any value that
// implements `Struct(any) error` — typically either a
// `*go-playground/validator.Validate` or a higher-level wrapper like
// a ModelValidator. nil restores the default: a
// go-playground validator that names fields by config key. Register
// [FieldName] on a custom validator to get the same naming.
func WithValidator(v Validator) Option {
	return func(o *options) {
		o.customValidator = v
	}
}

// WithLogger sets the logger for loader diagnostics and, when the options are
// passed to [NewReloader], for every reload event. Defaults to slog.Default().
func WithLogger(logger *slog.Logger) Option {
	return func(o *options) {
		o.logger = logger
	}
}

// GetConfig creates a koanf instance, loads sources in precedence order,
// unmarshals into T, injects secrets, runs any [WithPostLoad] hooks, and
// validates.
//
// ctx is propagated into secret resolution via the [SecretPopulator] passed
// to [WithSecretPopulator] and into every [WithPostLoad] hook; pass the
// caller's request/boot context so secret-fetch latency and cancellation flow
// through cleanly.
//
// Source precedence (highest wins):
//  1. Extra providers (CLI flags via kongprovider, custom providers)
//  2. Environment variables
//  3. Config files (merged left-to-right)
//  4. Programmatic defaults (WithDefaults)
//  5. Struct tag defaults (default:"value" via DefaultsFromTags)
//
// Every returned error carries both its category sentinel ([ErrFileRead],
// [ErrFileParse], [ErrSecretProvider], [ErrPostLoad], [ErrValidation] — the
// last also for a value that cannot decode into its field's type) and the
// underlying cause, so errors.Is and errors.As reach either.
func GetConfig[T any](ctx context.Context, opts ...Option) (*T, error) {
	cfg, _, err := load[T](ctx, buildOptions(opts))
	return cfg, err
}

// load is GetConfig's pipeline. It also reports which config files it read,
// which a Reloader needs to keep an optional file from vanishing unnoticed.
func load[T any](ctx context.Context, o *options) (*T, []string, error) {
	// A fresh instance per load: nothing from an earlier load (a key since
	// deleted from a file, say) can survive into this one.
	k := koanf.New(".")

	fields := Fields[T]()
	var sources, files []string

	// Step 1: struct tag defaults (lowest precedence).
	if tagDefaults := defaultsFrom(fields); len(tagDefaults) > 0 {
		if err := k.Load(confmap.Provider(tagDefaults, "."), nil); err != nil {
			return nil, nil, fmt.Errorf("loading struct tag defaults: %w", err)
		}
		sources = append(sources, "struct-tag-defaults")
	}

	// Step 2: programmatic defaults (override struct tag defaults).
	if len(o.defaults) > 0 {
		if err := k.Load(confmap.Provider(o.defaults, "."), nil); err != nil {
			return nil, nil, fmt.Errorf("loading defaults: %w", err)
		}
		sources = append(sources, "programmatic-defaults")
	}

	// Step 3: config files (merged left-to-right).
	for _, cf := range o.configFiles {
		err := k.Load(file.Provider(cf.path), yaml.Parser())
		switch {
		case err == nil:
			files = append(files, cf.path)
			sources = append(sources, "file:"+cf.path)
		case !cf.required && errors.Is(err, fs.ErrNotExist):
			// Absence is the one failure an optional file is allowed.
		default:
			return nil, nil, fileLoadError(cf.path, err)
		}
	}

	// Step 4: env vars, named from T's fields so a key with an underscore in
	// it (koanf:"max_conns") keeps it; see envResolver.
	if o.envPrefix != "" {
		prefix := strings.TrimSuffix(o.envPrefix, "_")
		fullPrefix := prefix + "_"
		resolve := newEnvResolver(fields, prefix)

		if err := k.Load(kenv.Provider(fullPrefix, ".", resolve.path), nil); err != nil {
			return nil, nil, fmt.Errorf("loading env vars: %w", err)
		}
		sources = append(sources, "env:"+fullPrefix+"*")
	}

	// Step 5: extra providers (including kongprovider), loaded in order added (highest precedence).
	for i, p := range o.extraProviders {
		if err := k.Load(p, nil); err != nil {
			return nil, nil, fmt.Errorf("loading extra provider %d: %w", i, err)
		}
		sources = append(sources, fmt.Sprintf("provider-%d", i))
	}

	o.logger.DebugContext(ctx, "config sources loaded", "sources", sources)

	// Step 6: unmarshal with explicit decode hooks. koanf v2 defaults do NOT
	// include StringToSliceHookFunc, so comma-separated env vars / defaults
	// would not be split into []string without this.
	cfg := new(T)
	if err := k.UnmarshalWithConf("", cfg, koanf.UnmarshalConf{
		DecoderConfig: &mapstructure.DecoderConfig{
			DecodeHook: mapstructure.ComposeDecodeHookFunc(
				mapstructure.StringToTimeDurationHookFunc(),
				mapstructure.StringToSliceHookFunc(","),
				mapstructure.TextUnmarshallerHookFunc(),
			),
			WeaklyTypedInput: true,
			Result:           cfg,
			TagName:          "koanf",
		},
	}); err != nil {
		// A value that cannot decode into its field's type (port: abc) is
		// invalid configuration; the decoder's error names the key.
		return nil, nil, fmt.Errorf("unmarshaling config: %w: %w", ErrValidation, err)
	}

	// Step 7: secret injection -- delegates to the consumer-supplied
	// populator with the caller's ctx, so secret-fetch latency and
	// cancellation reach the backend.
	if o.populateSecrets != nil {
		if err := o.populateSecrets(ctx, cfg); err != nil {
			return nil, nil, fmt.Errorf("populating secrets: %w: %w", ErrSecretProvider, err)
		}
	}

	// Step 8: post-load derivation / normalization. Runs after secrets so a
	// hook can derive from resolved credentials, and before validation so the
	// validator judges the normalized struct rather than the raw one.
	for i, hook := range o.postLoad {
		if err := hook(ctx, cfg); err != nil {
			if errors.Is(err, ErrPostLoad) {
				return nil, nil, err
			}
			return nil, nil, fmt.Errorf("%w: hook %d: %w", ErrPostLoad, i, err)
		}
	}

	// Step 9: validation (includes required field checks via validate:"required").
	// Field values are never logged or put in the message: a failing field
	// may be a credential.
	if !o.skipValidation {
		var v Validator = o.customValidator
		if v == nil {
			v = defaultValidator()
		}
		if err := validate(ctx, v, cfg); err != nil {
			// The two shapes pkgconfig knows how to unpack:
			//   1. validator.ValidationErrors (raw go-playground)
			//   2. anything else (typed wrappers like
			//      a ModelValidator →
			//      `*errutil.APIError`) — surfaced verbatim through
			//      validationError so callers can errors.As-extract it.
			var ve validator.ValidationErrors
			if errors.As(err, &ve) {
				return nil, nil, &validationError{
					msg:      fmt.Sprintf("validating config: %s: %s", ErrValidation, fieldErrorSummary(ve)),
					sentinel: ErrValidation,
					fieldErr: ve,
				}
			}
			return nil, nil, &validationError{
				msg:      fmt.Sprintf("validating config: %s: %v", ErrValidation, err),
				sentinel: ErrValidation,
				cause:    err,
			}
		}
	}

	return cfg, files, nil
}

// ctxValidator is the optional context-aware form of [Validator]
// (a ModelValidator implements it): its rules and message
// translation see the load's ctx.
type ctxValidator interface {
	StructCtx(ctx context.Context, v any) error
}

func validate(ctx context.Context, v Validator, cfg any) error {
	if cv, ok := v.(ctxValidator); ok {
		return cv.StructCtx(ctx, cfg)
	}
	return v.Struct(cfg)
}

// fileLoadError classifies a failed config file load. koanf.Load returns the
// file provider's *fs.PathError unwrapped when the read itself fails;
// anything else came from the YAML parser or the merge that follows it.
func fileLoadError(path string, err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return fmt.Errorf("loading config file %s: %w: %w", path, ErrFileRead, err)
	}
	return fmt.Errorf("loading config file %s: %w: %w", path, ErrFileParse, err)
}

// defaultValidator is the validator GetConfig uses when WithValidator sets
// none. It names fields by config key (see [FieldName]). A Validate is safe
// for concurrent use and caches struct metadata, so one is shared.
var defaultValidator = sync.OnceValue(func() *validator.Validate {
	v := validator.New()
	v.RegisterTagNameFunc(FieldName)
	return v
})

// fieldErrorSummary builds a compact summary of validation field errors
// for inclusion in the error message (e.g., "name(required), db.port(min=1)").
func fieldErrorSummary(ve validator.ValidationErrors) string {
	parts := make([]string, 0, len(ve))
	for _, fe := range ve {
		tag := fe.Tag()
		if fe.Param() != "" {
			tag += "=" + fe.Param()
		}
		// Namespace is "<Root>.a.b"; the root type name is noise.
		ns := fe.Namespace()
		if _, rest, ok := strings.Cut(ns, "."); ok {
			ns = rest
		}
		parts = append(parts, ns+"("+tag+")")
	}
	return strings.Join(parts, ", ")
}
