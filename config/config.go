package config

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/go-playground/validator/v10"
	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"
)

// Load builds a configuration of type T: it merges the sources in
// precedence order, decodes them into a new T, then resolves secrets, runs
// the post-load hooks and validates.
//
// Source precedence, highest first:
//
//  1. Override providers ([WithOverrideProvider], including kongprovider's CLI flags)
//  2. Environment variables ([WithEnvPrefix])
//  3. Config files ([WithFile], [WithRequiredFile]), a later file winning
//  4. Base providers ([WithBaseProvider])
//  5. Programmatic defaults ([WithDefaults])
//  6. Struct-tag defaults (default:"value")
//
// ctx reaches the secret populator, every post-load hook, a context-aware
// validator and every [ContextProvider].
//
// Every error wraps one category sentinel ([ErrFileRead], [ErrFileParse],
// [ErrSource], [ErrSecretProvider], [ErrPostLoad], [ErrValidation]) and its
// cause, so errors.Is and errors.As reach either. No error quotes a value
// meant for a sensitive field.
func Load[T any](ctx context.Context, opts ...Option) (*T, error) {
	cfg, _, err := load[T](ctx, buildOptions(opts))
	return cfg, err
}

// ContextProvider is a koanf.Provider whose reads take a context. A provider
// given to [WithBaseProvider] or [WithOverrideProvider] that implements it is
// read through ReadContext or ReadBytesContext with the load's context, so a
// remote read stops when the load is cancelled — by its caller, or by a
// Reloader's Stop.
type ContextProvider interface {
	koanf.Provider
	ReadContext(ctx context.Context) (map[string]any, error)
	ReadBytesContext(ctx context.Context) ([]byte, error)
}

// boundProvider reads a ContextProvider with one load's context.
type boundProvider struct {
	ContextProvider
	ctx context.Context
}

func (b boundProvider) Read() (map[string]any, error) { return b.ReadContext(b.ctx) }
func (b boundProvider) ReadBytes() ([]byte, error)    { return b.ReadBytesContext(b.ctx) }

// fileDigests maps each config file a load read to the SHA-256 of the exact
// bytes it parsed.
type fileDigests map[string][sha256.Size]byte

// loader merges one load's sources into k, each through its own koanf
// instance so its keys can be checked before they merge.
type loader struct {
	k       *koanf.Koanf
	root    reflect.Type
	strict  bool
	sources []string
}

// add loads p into a fresh instance and merges it into the load. A strict
// load first refuses any key the config type has no field for.
func (l *loader) add(name string, p koanf.Provider, parser koanf.Parser) (loadErr, keyErr error) {
	src := koanf.New(".")
	if err := src.Load(p, parser); err != nil {
		return err, nil
	}
	if l.strict {
		if unknown := unknownKeys(l.root, src.Keys()); len(unknown) > 0 {
			return nil, fmt.Errorf("keys no field of %s decodes: %s", l.root, strings.Join(unknown, ", "))
		}
	}
	if err := l.k.Merge(src); err != nil {
		return err, nil
	}
	l.sources = append(l.sources, name)
	return nil, nil
}

// load is Load's pipeline. It also reports which config files it read and
// what they held, which a Reloader needs to keep an optional file from
// vanishing unnoticed and to tell whether a file changed since.
func load[T any](ctx context.Context, o *options) (*T, fileDigests, error) {
	root := reflect.TypeFor[T]()
	if err := o.checkHookTypes(reflect.TypeFor[*T]()); err != nil {
		return nil, nil, err
	}
	// A fresh instance per load: nothing from an earlier load (a key since
	// deleted from a file, say) can survive into this one.
	l := &loader{k: koanf.New("."), root: root, strict: o.strictKeys}
	fields := Fields[T]()
	files := fileDigests{}

	if tagDefaults := defaultsFrom(fields); len(tagDefaults) > 0 {
		if err, _ := l.add("struct-tag defaults", confmap.Provider(tagDefaults, "."), nil); err != nil {
			return nil, nil, fmt.Errorf("loading struct-tag defaults: %w: %w", ErrSource, err)
		}
	}
	if len(o.defaults) > 0 {
		if err := sourceError("WithDefaults", ErrSource)(l.add("WithDefaults", confmap.Provider(o.defaults, "."), nil)); err != nil {
			return nil, nil, err
		}
	}
	for i, src := range o.base {
		name := fmt.Sprintf("base provider %d", i)
		if err := sourceError(name, ErrSource)(l.add(name, bind(ctx, src.provider), src.parser)); err != nil {
			return nil, nil, err
		}
	}
	for _, f := range o.files {
		data, err := os.ReadFile(f.path)
		switch {
		case err == nil:
		case !f.required && errors.Is(err, fs.ErrNotExist):
			continue // absence is the one failure an optional file is allowed
		default:
			return nil, nil, fmt.Errorf("loading config file %s: %w: %w", f.path, ErrFileRead, err)
		}
		if err := sourceError("config file "+f.path, ErrFileParse)(l.add(f.path, bytesProvider(data), o.fileParser)); err != nil {
			return nil, nil, err
		}
		files[f.path] = sha256.Sum256(data)
	}
	if o.envPrefix != "" {
		// Named from T's fields so a key with an underscore in it
		// (koanf:"max_conns") keeps it; see envSchema.
		schema := newEnvSchema(fields, strings.TrimSuffix(o.envPrefix, "_"))
		values, unmatched, err := schema.read(o.environ())
		if err != nil {
			return nil, nil, fmt.Errorf("loading env vars: %w: %w", ErrValidation, err)
		}
		if len(unmatched) > 0 {
			o.logger.DebugContext(ctx, "environment variables under the prefix name no config key", "names", unmatched)
		}
		if err, _ := l.add("env "+schema.prefix+"*", confmap.Provider(values, "."), nil); err != nil {
			return nil, nil, fmt.Errorf("loading env vars: %w: %w", ErrSource, err)
		}
	}
	for i, src := range o.overrides {
		name := fmt.Sprintf("override provider %d", i)
		if err := sourceError(name, ErrSource)(l.add(name, bind(ctx, src.provider), src.parser)); err != nil {
			return nil, nil, err
		}
	}
	o.logger.DebugContext(ctx, "config sources loaded", "sources", l.sources)

	// Decode. koanf's defaults do not split comma-separated strings into
	// slices, so env values and tag defaults need the slice hook.
	cfg := new(T)
	hooks := append(slices.Clone(o.decodeHooks),
		mapstructure.StringToTimeDurationHookFunc(),
		mapstructure.StringToSliceHookFunc(","),
		mapstructure.TextUnmarshallerHookFunc(),
	)
	if err := l.k.UnmarshalWithConf("", cfg, koanf.UnmarshalConf{
		DecoderConfig: &mapstructure.DecoderConfig{
			DecodeHook:       mapstructure.ComposeDecodeHookFunc(hooks...),
			WeaklyTypedInput: true,
			Result:           cfg,
			TagName:          "koanf",
		},
	}); err != nil {
		// A value that cannot decode into its field's type (port: abc) is
		// invalid configuration; the error names the key, and never the
		// value of a sensitive one.
		return nil, nil, fmt.Errorf("unmarshaling config: %w: %w", ErrValidation, withholdSensitiveDecodeErrors(root, err))
	}

	if o.secrets != nil {
		if err := o.secrets(ctx, cfg); err != nil {
			return nil, nil, fmt.Errorf("populating secrets: %w: %w", ErrSecretProvider, err)
		}
	}

	// After secrets, so a hook can derive from resolved credentials; before
	// validation, so the validator judges the normalized struct.
	for i, hook := range o.postLoad {
		if err := hook.run(ctx, cfg); err != nil {
			return nil, nil, fmt.Errorf("%w: hook %d: %w", ErrPostLoad, i, err)
		}
	}

	// Field values are never logged or put in the message: a failing field
	// may be a credential.
	if !o.skipValid {
		v := o.validator
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
				summary = fieldErrorSummary(root, ve)
			}
			return nil, nil, &validationError{
				msg:   fmt.Sprintf("validating config: %s: %s", ErrValidation, summary),
				cause: err,
			}
		}
	}

	return cfg, files, nil
}

// sourceError classifies what loader.add returned for the source called
// name: a load failure in category, an unknown key in ErrValidation.
func sourceError(name string, category error) func(loadErr, keyErr error) error {
	return func(loadErr, keyErr error) error {
		switch {
		case loadErr != nil:
			return fmt.Errorf("loading %s: %w: %w", name, category, loadErr)
		case keyErr != nil:
			return fmt.Errorf("loading %s: %w: %w", name, ErrValidation, keyErr)
		}
		return nil
	}
}

// bind gives a ContextProvider the load's context.
func bind(ctx context.Context, p koanf.Provider) koanf.Provider {
	if cp, ok := p.(ContextProvider); ok {
		return boundProvider{ContextProvider: cp, ctx: ctx}
	}
	return p
}

// unknownKeys returns the keys, of those a source set, that no field of root
// decodes. Matching is case-insensitive, as decoding is.
func unknownKeys(root reflect.Type, keys []string) []string {
	var unknown []string
	for _, key := range keys {
		if !keyDecodes(root, strings.Split(key, ".")) {
			unknown = append(unknown, key)
		}
	}
	return unknown
}

// keyDecodes reports whether the key path segs reaches a field of t, or
// lands beneath a map, an interface or a struct that keeps unknown keys in a
// koanf:",remain" field.
func keyDecodes(t reflect.Type, segs []string) bool {
	for _, seg := range segs {
		t = derefType(t)
		switch {
		case t.Kind() == reflect.Interface:
			return true
		case t.Kind() == reflect.Map:
			t = t.Elem()
		case isBranch(t):
			f, ok := fieldByKeyFold(t, seg)
			if !ok {
				return hasRemain(t)
			}
			t = f.Type
		default:
			return false // a leaf has no keys beneath it
		}
	}
	return true
}

// fieldByKeyFold is fieldByKey falling back to a case-insensitive match.
func fieldByKeyFold(t reflect.Type, key string) (reflect.StructField, bool) {
	if f, ok := fieldByKey(t, key); ok {
		return f, true
	}
	for kf := range keyedFields(t) {
		switch {
		case kf.key != "" && strings.EqualFold(kf.key, key):
			return kf.field, true
		case kf.key == "" && isBranch(derefType(kf.field.Type)):
			if f, ok := fieldByKeyFold(derefType(kf.field.Type), key); ok {
				return f, true
			}
		}
	}
	return reflect.StructField{}, false
}

// hasRemain reports whether struct t, or one squashed into it, collects
// unknown keys in a koanf:",remain" field.
func hasRemain(t reflect.Type) bool {
	for i := range t.NumField() {
		f := t.Field(i)
		_, opts, _ := strings.Cut(f.Tag.Get("koanf"), ",")
		tagOpts := strings.Split(opts, ",")
		switch {
		case slices.Contains(tagOpts, "remain"):
			return true
		case slices.Contains(tagOpts, "squash") && isBranch(derefType(f.Type)) && hasRemain(derefType(f.Type)):
			return true
		}
	}
	return false
}

// bytesProvider hands koanf the bytes a load read, so the digest it records
// is of exactly what was parsed.
type bytesProvider []byte

func (b bytesProvider) ReadBytes() ([]byte, error) { return b, nil }
func (b bytesProvider) Read() (map[string]any, error) {
	return nil, errors.New("config: bytesProvider needs a parser")
}

// defaultValidator is the validator Load uses when WithValidator sets none.
// It names fields by config key (see [FieldName]). A Validate is safe for
// concurrent use and caches struct metadata, so one is shared.
var defaultValidator = sync.OnceValue(func() *validator.Validate {
	v := validator.New()
	v.RegisterTagNameFunc(FieldName)
	return v
})

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
