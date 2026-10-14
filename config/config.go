package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/go-playground/validator/v10"
	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"
)

// Load builds a configuration of type T: it merges the sources in
// precedence order, decodes them into a new T — failing on a key no field
// decodes, unless [WithAllowUnknownKeys] — then resolves secrets, runs the
// post-load hooks, validates with the validator and, when *T is
// [Validatable], with its Validate method.
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
// Keys match fields regardless of letter case, as decoding does. Each
// source's keys are rewritten into the fields' own spelling before the
// sources merge, so a higher source wins whatever case it used; a source that
// spells one field two ways fails with [ErrValidation]. An element struct's
// `default` tags apply to every map entry and slice element the sources
// create, below what they set.
//
// Every error wraps one category sentinel ([ErrFileRead], [ErrFileParse],
// [ErrSource], [ErrSecretProvider], [ErrPostLoad], [ErrValidation]) and its
// cause, so errors.Is and errors.As reach either. The errors this package
// writes quote no value meant for a sensitive field: a decode failure on a
// field that is, or could hold, a sensitive value — a decode hook's failure
// included — names the field and type only, and a parse failure names the
// source and line only (see [ParseError]). A failure on an ordinary field
// keeps its decoder's message, which may quote the value (time.Parse does).
// Errors returned by the caller's own providers, secret populator, post-load
// hooks, validator and Validate method are passed through as they were
// written.
func Load[T any](ctx context.Context, opts ...Option) (*T, error) {
	l, err := load[T](ctx, buildOptions(opts))
	return l.cfg, err
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

// loader merges one load's sources into k. Each source's tree is read
// whole, its struct keys rewritten into the fields' own spelling (keyWalk),
// and then merged, so precedence holds whatever letter case a source used.
// With provenance, origins records what set each value as the trees merge.
type loader struct {
	k       *koanf.Koanf
	root    reflect.Type
	sources []string
	origins *originNode // nil without provenance
}

// add merges data, the tree of the source called name, whose values label
// attributes. A source that sets one field under two spellings fails.
func (l *loader) add(name string, data map[string]any, label labeler) error {
	var w keyWalk
	tree := w.tree(l.root, data)
	if len(w.twice) > 0 {
		return fmt.Errorf("loading %s: %w: keys spelled two ways set one field: %s", name, ErrValidation, strings.Join(w.twice, ", "))
	}
	return l.merge(name, tree, label)
}

func (l *loader) merge(name string, tree map[string]any, label labeler) error {
	if err := l.k.Load(confmap.Provider(tree, ""), nil); err != nil {
		return fmt.Errorf("loading %s: %w: %w", name, ErrSource, err)
	}
	if l.origins != nil {
		l.origins.merge(tree, label, nil)
	}
	l.sources = append(l.sources, name)
	return nil
}

// addProvider reads a provider — a map, or bytes through parser — and adds
// it. A read failure is ErrSource with the provider's error; bytes the parser
// rejects are ErrSource with a *ParseError. With provenance, an
// OriginProvider's annotations name the source of each value; any other
// provider is src's own.
func (l *loader) addProvider(ctx context.Context, name string, p providerSource, src Source) error {
	if op, ok := p.provider.(OriginProvider); ok && p.parser == nil && l.origins != nil {
		data, annotations, err := op.ReadWithOrigins(ctx)
		if err != nil {
			return fmt.Errorf("loading %s: %w: %w", name, ErrSource, err)
		}
		var w keyWalk
		tree := w.tree(l.root, data)
		if len(w.twice) > 0 {
			return fmt.Errorf("loading %s: %w: keys spelled two ways set one field: %s", name, ErrValidation, strings.Join(w.twice, ", "))
		}
		label, err := annotationLabeler(l.root, tree, annotations, src)
		if err != nil {
			return fmt.Errorf("loading %s: %w: %w", name, ErrSource, err)
		}
		return l.merge(name, tree, label)
	}
	pr := bind(ctx, p.provider)
	var data map[string]any
	if p.parser == nil {
		m, err := pr.Read()
		if err != nil {
			return fmt.Errorf("loading %s: %w: %w", name, ErrSource, err)
		}
		data = m
	} else {
		b, err := pr.ReadBytes()
		if err != nil {
			return fmt.Errorf("loading %s: %w: %w", name, ErrSource, err)
		}
		if data, err = parse(name, p.parser, b); err != nil {
			return fmt.Errorf("loading %s: %w: %w", name, ErrSource, err)
		}
	}
	return l.add(name, data, constant(src))
}

// addFlat adds a map keyed by dot-path.
func (l *loader) addFlat(name string, flat map[string]any, label labeler) error {
	data, err := confmap.Provider(flat, ".").Read()
	if err != nil {
		return fmt.Errorf("loading %s: %w: %w", name, ErrSource, err)
	}
	return l.add(name, data, label)
}

// loaded is one load's outcome: the config, the config files it read and
// what they held — which a Reloader needs to keep an optional file from
// vanishing unnoticed and to tell whether a file changed since — and, with
// provenance, where each key's value came from.
type loaded[T any] struct {
	cfg     *T
	files   fileDigests
	origins map[string]Origin
}

// load is Load's pipeline.
func load[T any](ctx context.Context, o *options) (loaded[T], error) {
	none := loaded[T]{}
	root := reflect.TypeFor[T]()
	if err := o.checkHookTypes(reflect.TypeFor[*T]()); err != nil {
		return none, err
	}
	schema, err := rootSchema(root)
	if err != nil {
		return none, fmt.Errorf("loading config: %w", err)
	}
	// A fresh instance per load: nothing from an earlier load (a key since
	// deleted from a file, say) can survive into this one.
	l := &loader{k: koanf.New("."), root: root}
	var rec *originRecorder
	if o.provenance {
		rec = &originRecorder{schema: schema, tree: newOriginRoot()}
		l.origins = rec.tree
	}
	fields := Fields[T]()
	files := fileDigests{}

	if tagDefaults := defaultsFrom(fields); len(tagDefaults) > 0 {
		if err := l.addFlat("struct-tag defaults", tagDefaults, constant(Source{Kind: SourceTag})); err != nil {
			return none, err
		}
	}
	if len(o.defaults) > 0 {
		if err := l.addFlat("WithDefaults", o.defaults, constant(Source{Kind: SourceDefaults})); err != nil {
			return none, err
		}
	}
	for i, src := range o.base {
		if err := l.addProvider(ctx, fmt.Sprintf("base provider %d", i), src, positionSource(SourceBase, i)); err != nil {
			return none, err
		}
	}
	for _, f := range o.files {
		raw, err := os.ReadFile(f.path)
		switch {
		case err == nil:
		case !f.required && errors.Is(err, fs.ErrNotExist):
			continue // absence is the one failure an optional file is allowed
		default:
			return none, fmt.Errorf("loading config file %s: %w: %w", f.path, ErrFileRead, err)
		}
		data, err := parse(f.path, o.fileParser, raw)
		if err != nil {
			return none, fmt.Errorf("loading config file %s: %w: %w", f.path, ErrFileParse, err)
		}
		if err := l.add("config file "+f.path, data, constant(Source{Kind: SourceFile, Name: f.path})); err != nil {
			return none, err
		}
		files[f.path] = sha256.Sum256(raw)
	}
	if o.envPrefix != "" {
		// Named from T's fields so a key with an underscore in it
		// (koanf:"max_conns") keeps it; see envSchema.
		schema := newEnvSchema(fields, strings.TrimSuffix(o.envPrefix, "_"))
		values, names, unmatched, err := schema.read(o.environ())
		if err != nil {
			return none, fmt.Errorf("loading env vars: %w: %w", ErrValidation, err)
		}
		if len(unmatched) > 0 {
			o.log().DebugContext(ctx, "environment variables under the prefix name no config key", "names", unmatched)
		}
		if err := l.addFlat("env "+schema.prefix+"*", values, envLabeler(schema.prefix, names)); err != nil {
			return none, err
		}
	}
	for i, src := range o.overrides {
		if err := l.addProvider(ctx, fmt.Sprintf("override provider %d", i), src, positionSource(SourceOverride, i)); err != nil {
			return none, err
		}
	}
	o.log().DebugContext(ctx, "config sources loaded", "sources", l.sources)

	merged := l.k.Raw()
	fillElementDefaults(root, merged)
	if l.origins != nil {
		l.origins.fill(merged, nil)
	}
	cfg := new(T)
	// The decoder's own record of the keys it left unused is what strict
	// keys checks, so strictness follows decoding exactly: squash, remain,
	// custom decoders, decode hooks and weak typing included.
	var md mapstructure.Metadata
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		DecodeHook:       o.decodeHook(),
		WeaklyTypedInput: true,
		Result:           cfg,
		TagName:          "koanf",
		Metadata:         &md,
	})
	if err != nil {
		return none, fmt.Errorf("unmarshaling config: %w", err)
	}
	if err := dec.Decode(merged); err != nil {
		// A value that cannot decode into its field's type (port: abc) is
		// invalid configuration; the error names the key, and never the
		// value of a field that is, or could hold, a sensitive one.
		return none, fmt.Errorf("unmarshaling config: %w: %w", ErrValidation, decodeErrors(schema, merged, err))
	}
	if !o.allowUnknown && len(md.Unused) > 0 {
		unused := make([]string, len(md.Unused))
		for i, name := range md.Unused {
			unused[i] = unusedKey(schema, merged, name)
		}
		slices.Sort(unused)
		return none, fmt.Errorf("unmarshaling config: %w: keys no field of %s decodes: %s",
			ErrValidation, root, strings.Join(unused, ", "))
	}

	// stage runs one stage that may change the decoded config, recording
	// what it changed when provenance is on.
	cv := reflect.ValueOf(cfg).Elem()
	stage := func(src Source, run func() error) error {
		if rec == nil {
			return run()
		}
		if err := rec.snapshot(cv); err != nil {
			return fmt.Errorf("recording provenance: %w", err)
		}
		if err := run(); err != nil {
			return err
		}
		if err := rec.changed(cv, src); err != nil {
			return fmt.Errorf("recording provenance: %w", err)
		}
		return nil
	}
	if o.secrets != nil {
		if err := stage(Source{Kind: SourceSecrets}, func() error {
			if err := o.secrets(ctx, cfg); err != nil {
				return fmt.Errorf("populating secrets: %w: %w", ErrSecretProvider, err)
			}
			return nil
		}); err != nil {
			return none, err
		}
	}

	// After secrets, so a hook can derive from resolved credentials; before
	// validation, so the validator judges the normalized struct.
	for i, hook := range o.postLoad {
		if err := stage(positionSource(SourcePostLoad, i), func() error {
			if err := hook.run(ctx, cfg); err != nil {
				return fmt.Errorf("%w: hook %d: %w", ErrPostLoad, i, err)
			}
			return nil
		}); err != nil {
			return none, err
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
				summary = fieldErrorSummary(reflect.ValueOf(cfg), ve)
			}
			return none, &validationError{
				msg:   fmt.Sprintf("validating config: %s: %s", ErrValidation, summary),
				cause: err,
			}
		}
		if v, ok := any(cfg).(Validatable); ok {
			if err := v.Validate(); err != nil {
				return none, &validationError{
					msg:   fmt.Sprintf("validating config: %s: %s", ErrValidation, err),
					cause: err,
				}
			}
		}
	}

	out := loaded[T]{cfg: cfg, files: files}
	if rec != nil {
		if out.origins, err = rec.origins(cv); err != nil {
			return none, fmt.Errorf("recording provenance: %w", err)
		}
	}
	return out, nil
}

// bind gives a ContextProvider the load's context.
func bind(ctx context.Context, p koanf.Provider) koanf.Provider {
	if cp, ok := p.(ContextProvider); ok {
		return boundProvider{ContextProvider: cp, ctx: ctx}
	}
	return p
}

// ParseError reports bytes a parser rejected: a config file, or a provider
// loaded with a parser. Its message names the line when the parser gave one,
// and leaves out the parser's own message, which may quote the source — a
// credential in a file whose syntax broke around it. Err is that message's
// error, for a diagnostic shown only where the source may be; errors.Unwrap
// and errors.As reach it.
type ParseError struct {
	// Source names what was parsed: a file path, or "base provider 0".
	Source string
	// Line is the line the parser reported, or 0.
	Line int
	// Err is the parser's error.
	Err error
}

func (e *ParseError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s does not parse at line %d (the parser's message may quote the source and is not shown)", e.Source, e.Line)
	}
	return e.Source + " does not parse (the parser's message may quote the source and is not shown)"
}

func (e *ParseError) Unwrap() error { return e.Err }

// parse unmarshals the bytes of the source called name.
func parse(name string, parser koanf.Parser, b []byte) (map[string]any, error) {
	data, err := parser.Unmarshal(b)
	if err != nil {
		return nil, &ParseError{Source: name, Line: parserLine(err, b), Err: err}
	}
	return data, nil
}

// parserLine is the line a parser's message names where the message itself
// says where it is — "yaml: line 3: ...", or "line 3: ..." opening one of its
// lines — and the source has that many lines; otherwise 0. A number the
// parser quoted from the source sits elsewhere in the message and is not
// taken for a location.
func parserLine(err error, source []byte) int {
	m := lineInMessage.FindStringSubmatch(err.Error())
	if m == nil {
		return 0
	}
	n, convErr := strconv.Atoi(m[1])
	if convErr != nil || n < 1 || n > bytes.Count(source, []byte("\n"))+1 {
		return 0
	}
	return n
}

var lineInMessage = regexp.MustCompile(`(?m)^\s*(?:[a-z]+: )?line (\d+):`)

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
// naming each field by its key in cfg, the validated config.
func fieldErrorSummary(cfg reflect.Value, ve validator.ValidationErrors) string {
	parts := make([]string, 0, len(ve))
	for _, fe := range ve {
		tag := fe.Tag()
		if fe.Param() != "" {
			tag += "=" + fe.Param()
		}
		parts = append(parts, validationKey(cfg, fe)+"("+tag+")")
	}
	return strings.Join(parts, ", ")
}

// validationKey names the field a validation error is about by its key.
func validationKey(cfg reflect.Value, fe validator.FieldError) string {
	p, exact := validatorKey(cfg, fe.StructNamespace())
	switch {
	case exact:
		return p.orRoot()
	case len(p) > 0:
		return p.String() + " (exact location unavailable)"
	}
	// Not a namespace of cfg: the validator's own, minus the root type name.
	ns := fe.Namespace()
	if _, rest, cut := strings.Cut(ns, "."); cut {
		ns = rest
	}
	return ns
}
