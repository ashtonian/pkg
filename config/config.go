package config

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"reflect"
	"strings"
	"sync"

	"github.com/go-playground/validator/v10"
	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
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
	environ         func() []string
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
	if o.environ == nil {
		o.environ = os.Environ
	}
	return o
}

// requireFiles marks every configured file that loaded as required. A
// Reloader uses it so a file that loaded once cannot silently drop out of a
// reload.
func (o *options) requireFiles(loaded fileDigests) {
	for i := range o.configFiles {
		if _, ok := loaded[o.configFiles[i].path]; ok {
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

// WithEnviron sets where environment variables are read from: a function
// returning "NAME=value" entries, as [os.Environ] does, called on every load
// and reload. The default is os.Environ. Tests and examples pass a fixed list;
// a deployment adapter can pass variables gathered elsewhere.
func WithEnviron(environ func() []string) Option {
	return func(o *options) {
		o.environ = environ
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
// shared key. The values are deep-copied when WithDefaults is called — every
// map, slice, array and pointer target reachable through exported fields —
// so changing the caller's data afterwards changes nothing, including what a
// Reloader built from the option reads later. Unexported struct fields,
// channels and functions are copied by value.
func WithDefaults(defaults map[string]any) Option {
	snapshot := deepCopy(reflect.ValueOf(defaults)).Interface().(map[string]any)
	return func(o *options) {
		if o.defaults == nil {
			o.defaults = make(map[string]any, len(snapshot))
		}
		maps.Copy(o.defaults, snapshot)
	}
}

// deepCopy returns a copy of v sharing no map, slice, array element or
// pointer target with it. Data v shares internally, cycles included, is
// shared the same way in the copy.
func deepCopy(v reflect.Value) reflect.Value {
	c := copier{made: map[copyKey]reflect.Value{}}
	return c.copy(v)
}

type copier struct {
	made map[copyKey]reflect.Value
}

// copyKey identifies a map, slice or pointer already copied. A slice's
// length is part of it: two slices over one array are different values.
type copyKey struct {
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
		key := copyKey{ptr: v.Pointer(), typ: v.Type()}
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
			for iter := v.MapRange(); iter.Next(); {
				m.SetMapIndex(iter.Key(), c.copy(iter.Value()))
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

// fileDigests maps each config file a load read to the SHA-256 of the exact
// bytes it parsed.
type fileDigests map[string][sha256.Size]byte

// load is GetConfig's pipeline. It also reports which config files it read
// and what they held, which a Reloader needs to keep an optional file from
// vanishing unnoticed and to tell whether a file changed since.
func load[T any](ctx context.Context, o *options) (*T, fileDigests, error) {
	// A fresh instance per load: nothing from an earlier load (a key since
	// deleted from a file, say) can survive into this one.
	k := koanf.New(".")

	fields := Fields[T]()
	var sources []string
	files := fileDigests{}

	// Step 1: struct tag defaults (lowest precedence).
	if tagDefaults := defaultsFrom(fields); len(tagDefaults) > 0 {
		if err := k.Load(confmap.Provider(tagDefaults, "."), nil); err != nil {
			return nil, nil, fmt.Errorf("loading struct tag defaults: %w: %w", ErrSource, err)
		}
		sources = append(sources, "struct-tag-defaults")
	}

	// Step 2: programmatic defaults (override struct tag defaults).
	if len(o.defaults) > 0 {
		if err := k.Load(confmap.Provider(o.defaults, "."), nil); err != nil {
			return nil, nil, fmt.Errorf("loading defaults: %w: %w", ErrSource, err)
		}
		sources = append(sources, "programmatic-defaults")
	}

	// Step 3: config files (merged left-to-right).
	for _, cf := range o.configFiles {
		data, err := os.ReadFile(cf.path)
		switch {
		case err == nil:
		case !cf.required && errors.Is(err, fs.ErrNotExist):
			continue // absence is the one failure an optional file is allowed
		default:
			return nil, nil, fileLoadError(cf.path, err)
		}
		if err := k.Load(bytesProvider(data), yaml.Parser()); err != nil {
			return nil, nil, fileLoadError(cf.path, err)
		}
		files[cf.path] = sha256.Sum256(data)
		sources = append(sources, "file:"+cf.path)
	}

	// Step 4: env vars, named from T's fields so a key with an underscore in
	// it (koanf:"max_conns") keeps it; see envSchema.
	if o.envPrefix != "" {
		schema := newEnvSchema(fields, strings.TrimSuffix(o.envPrefix, "_"))
		values, unmatched, err := schema.read(o.environ())
		if err != nil {
			return nil, nil, fmt.Errorf("loading env vars: %w: %w", ErrValidation, err)
		}
		if len(unmatched) > 0 {
			o.logger.DebugContext(ctx, "environment variables under the prefix name no config key", "names", unmatched)
		}
		if err := k.Load(confmap.Provider(values, "."), nil); err != nil {
			return nil, nil, fmt.Errorf("loading env vars: %w: %w", ErrSource, err)
		}
		sources = append(sources, "env:"+schema.prefix+"*")
	}

	// Step 5: extra providers (including kongprovider), loaded in order added (highest precedence).
	for i, p := range o.extraProviders {
		if err := k.Load(p, nil); err != nil {
			return nil, nil, fmt.Errorf("loading extra provider %d: %w: %w", i, ErrSource, err)
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
		// invalid configuration; the decoder's error names the key, and
		// never the value of a sensitive one.
		return nil, nil, fmt.Errorf("unmarshaling config: %w: %w", ErrValidation,
			withholdSensitiveDecodeErrors(reflect.TypeFor[T](), err))
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
			// The validator's error is kept whole as the cause, so a
			// wrapper's own sentinels and types stay reachable; go-playground
			// field errors found inside it only shape the message.
			summary := err.Error()
			var ve validator.ValidationErrors
			if errors.As(err, &ve) {
				summary = fieldErrorSummary(reflect.TypeFor[T](), ve)
			}
			return nil, nil, &validationError{
				msg:   fmt.Sprintf("validating config: %s: %s", ErrValidation, summary),
				cause: err,
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

// bytesProvider hands koanf the bytes a load read, so the digest it records
// is of exactly what was parsed.
type bytesProvider []byte

func (b bytesProvider) ReadBytes() ([]byte, error) { return b, nil }
func (b bytesProvider) Read() (map[string]any, error) {
	return nil, errors.New("config: bytesProvider needs a parser")
}

// fileLoadError classifies a failed config file load: a *fs.PathError is the
// read itself failing; anything else came from the YAML parser or the merge
// that follows it.
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
// for inclusion in the error message (e.g., "name(required), db.port(min=1)"),
// naming each field by its config key in root.
func fieldErrorSummary(root reflect.Type, ve validator.ValidationErrors) string {
	parts := make([]string, 0, len(ve))
	for _, fe := range ve {
		tag := fe.Tag()
		if fe.Param() != "" {
			tag += "=" + fe.Param()
		}
		ns, ok := configPath(root, fe.StructNamespace())
		if !ok {
			// Namespace is "<Root>.a.b"; the root type name is noise.
			ns = fe.Namespace()
			if _, rest, cut := strings.Cut(ns, "."); cut {
				ns = rest
			}
		}
		parts = append(parts, ns+"("+tag+")")
	}
	return strings.Join(parts, ", ")
}

// configPath translates a validator struct namespace — "Root.DB.Pools[0].Max",
// Go field names — into the config key path a [Fields] leaf carries,
// "db.pools[0].max", so a squashed struct contributes no segment. ok is false
// when the namespace does not resolve against root.
func configPath(root reflect.Type, structNS string) (path string, ok bool) {
	_, rest, cut := strings.Cut(structNS, ".")
	if !cut {
		return "", false
	}
	t := root
	for seg := range strings.SplitSeq(rest, ".") {
		name, index, _ := strings.Cut(seg, "[")
		t = derefType(t)
		if t.Kind() != reflect.Struct {
			return "", false
		}
		f, found := t.FieldByName(name)
		if !found {
			return "", false
		}
		key, skip := fieldKey(f)
		if skip {
			return "", false
		}
		path = joinPath(path, key)
		t = f.Type
		if index != "" {
			path += "[" + index
			for range strings.Count(seg, "[") {
				switch t = derefType(t); t.Kind() {
				case reflect.Slice, reflect.Array, reflect.Map:
					t = t.Elem()
				default:
					return "", false
				}
			}
		}
	}
	return path, true
}
