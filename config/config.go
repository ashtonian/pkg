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
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/go-viper/mapstructure/v2"
	kmaps "github.com/knadh/koanf/maps"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/v2"

	"github.com/ashtonian/pkg/config/internal/deepcopy"
	"github.com/ashtonian/pkg/gowalker"
)

// Load builds a configuration of type T: it merges the sources in
// precedence order, decodes them into a new T — failing on a key no field
// decodes, unless [WithUnknownKeys] decides otherwise — then resolves
// secrets, runs the post-load hooks, validates with the validator and, when
// *T is [Validatable], with its Validate method.
//
// Source precedence, highest first:
//
//  1. Override providers ([WithOverrideProvider], including kongprovider's CLI flags)
//  2. Environment variables ([WithEnvPrefix], [WithEnvPrefixes])
//  3. Config files ([WithFile], [WithRequiredFile]), a later file winning
//  4. Base providers ([WithBaseProvider])
//  5. Programmatic defaults ([WithDefaults])
//  6. Code defaults ([Defaulter])
//  7. Struct-tag defaults (default:"value")
//
// ctx reaches the secret populator, every post-load hook, a context-aware
// validator and every [ContextProvider]. Once ctx ends no further stage
// runs: a load whose context has ended reads no source and calls nothing of
// the caller's, and one that ends mid-load stops before the next provider
// or file, the environment, decoding, the variable filter, a report
// callback, the secret populator, the next post-load hook or validation,
// returning an error that wraps ctx.Err()
// and its [context.Cause]. A stage already running observes ctx itself.
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

// Decode builds a T from data — nested maps keyed by field key, as a parsed
// document or a plugin's settings block holds them — through the pipeline
// Load runs: tag defaults beneath it, aliases and separators, strict keys,
// decode hooks, secrets, post-load hooks and validation. data loads as a base
// provider and is copied; opts add to the load as they add to Load's, and a
// source they add loads above data.
//
//	type Plugin struct {
//	    Name    string        `koanf:"name" validate:"required"`
//	    Timeout time.Duration `koanf:"timeout" default:"5s"`
//	}
//	p, err := config.Decode[Plugin](ctx, map[string]any{"name": "audit"})
func Decode[T any](ctx context.Context, data map[string]any, opts ...Option) (*T, error) {
	src, err := NewMapProvider(func(context.Context) (map[string]any, error) { return data, nil }, "")
	if err != nil {
		return nil, err
	}
	return Load[T](ctx, append([]Option{WithBaseProvider(src, nil)}, opts...)...)
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
// whole and its struct keys rewritten into the fields' own spelling
// (keyWalk), so precedence holds whatever letter case a source used. Every
// source is read and prepared before any merges (see layers.go).
// With provenance, shadowed-input reporting or a source under
// WithUnknownEntries, origins records what set each value as the trees
// merge; with shadowed-input reporting, inputs records what each
// non-default source set.
type loader struct {
	k          *koanf.Koanf
	root       reflect.Type
	schema     *schemaNode
	sources    []string
	aliased    []string    // keys sources set under an alias tag's name, by source
	origins    *originNode // nil unless the load attributes values or a source claims its unknown keys
	provenance bool        // origins holds all provenance reads, not only what shadow detection does
	inputs     *inputs     // nil unless the load finds shadowed inputs
	// unkeyed is the koanf:"-" fields of the typed sections the merge
	// keeps, set on the decoded config (see unkeyed.go).
	unkeyed unkeyedSections
}

// rewrite is data as it merges: maps keyed by anything but strings keyed
// by their text, as koanf keys them, and every key naming a field spelled
// as the field is.
func (l *loader) rewrite(name string, data map[string]any) (map[string]any, error) {
	return l.walk(&keyWalk{schema: l.schema}, name, data)
}

// walk is rewrite through w, which then holds what the walk recorded.
func (l *loader) walk(w *keyWalk, name string, data map[string]any) (map[string]any, error) {
	tree := w.tree(l.root, textKeyed(data))
	if len(w.twice) > 0 {
		return nil, fmt.Errorf("loading %s: %w: keys spelled two ways set one field: %s", name, ErrValidation, strings.Join(w.twice, ", "))
	}
	if len(w.notJSON) > 0 {
		return nil, fmt.Errorf("loading %s: %w: format:\"json\" values that are not JSON: %s", name, ErrValidation, strings.Join(w.notJSON, ", "))
	}
	if len(w.noText) > 0 {
		return nil, fmt.Errorf("loading %s: %w: values that do not marshal to text: %s", name, ErrValidation, strings.Join(w.noText, ", "))
	}
	for _, a := range w.aliased {
		l.aliased = append(l.aliased, a+" in "+name)
	}
	return tree, nil
}

// koanfMerge is mergeTrees as koanf calls a merge function.
func koanfMerge(src, dest map[string]any) error {
	mergeTrees(src, dest)
	return nil
}

// merge merges tree into the load and, when the load attributes values, the
// same tree into the origins: what koanf merges and what is attributed are
// one tree. A source's rank is its position in the merge. Without
// provenance the origins serve shadow detection alone, which defaults never
// take part in: they merge first, so they hold nothing a source set.
func (l *loader) merge(name string, tree map[string]any, label labeler) error {
	return l.mergeSource(name, tree, nil, label)
}

// mergeSource is merge for a source whose walk recorded unkeyed, the
// koanf:"-" fields of the typed sections it sets.
func (l *loader) mergeSource(name string, tree map[string]any, unkeyed []unkeyedFields, label labeler) error {
	if err := l.k.Load(confmap.Provider(tree, ""), nil, koanf.WithMergeFunc(koanfMerge)); err != nil {
		return fmt.Errorf("loading %s: %w: %w", name, ErrSource, err)
	}
	l.unkeyed.merge(tree, unkeyed)
	rank := len(l.sources)
	if l.origins != nil && (l.provenance || !label.defaults()) {
		l.origins.merge(tree, &originMerge{label: label, rank: rank, provenance: l.provenance})
	}
	if l.inputs != nil {
		l.inputs.add(name, rank, tree, label)
	}
	l.sources = append(l.sources, name)
	return nil
}

// readProvider reads a provider — a map, or bytes through parser — or an
// entry source, and prepares it. A read failure is ErrSource with the
// provider's error; bytes the parser rejects are ErrSource with a
// *ParseError. When the load attributes values, an OriginProvider's
// annotations name the source of each value; any other provider is src's
// own.
func (l *loader) readProvider(ctx context.Context, name string, p providerSource, src Source) (*layer, error) {
	if p.entries != nil {
		return l.readEntries(ctx, name, p, src)
	}
	if op, ok := p.provider.(OriginProvider); ok && p.parser == nil && (l.provenance || l.inputs != nil) {
		data, annotations, err := op.ReadWithOrigins(ctx)
		if err != nil {
			return nil, fmt.Errorf("loading %s: %w: %w", name, ErrSource, err)
		}
		return l.prepareProvider(name, data, p, func(tree map[string]any) (labeler, error) {
			return annotationLabeler(l.schema, tree, annotations, src)
		})
	}
	pr := bind(ctx, p.provider)
	var data map[string]any
	if p.parser == nil {
		m, err := pr.Read()
		if err != nil {
			return nil, fmt.Errorf("loading %s: %w: %w", name, ErrSource, err)
		}
		data = m
	} else {
		b, err := pr.ReadBytes()
		if err != nil {
			return nil, fmt.Errorf("loading %s: %w: %w", name, ErrSource, err)
		}
		if data, err = parse(name, p.parser, b); err != nil {
			return nil, fmt.Errorf("loading %s: %w: %w", name, ErrSource, err)
		}
	}
	return l.prepareProvider(name, data, p, func(map[string]any) (labeler, error) { return constant(src), nil })
}

// addFlat adds a map keyed by dot-path, as prepareFlat prepares it.
func (l *loader) addFlat(name string, flat map[string]any, label labeler) error {
	y, err := l.prepareFlat(name, flat, label)
	if err != nil {
		return err
	}
	_, err = l.mergeLayer(y, nil)
	return err
}

// prepareFlat is prepare for a map keyed by dot-path. It is unflattened
// from a deep copy rather than through a koanf provider, whose copy keeps
// only exported fields: prepare must see the values whole to turn those
// that marshal to text into their text.
func (l *loader) prepareFlat(name string, flat map[string]any, label labeler) (*layer, error) {
	copied := deepcopy.Value(reflect.ValueOf(flat)).Interface().(map[string]any)
	return l.prepare(name, kmaps.Unflatten(copied, "."), label)
}

// loaded is one load's outcome: the config, the config files it read and
// what they held — which a Reloader needs to keep an optional file from
// vanishing unnoticed and to tell whether a file changed since — with
// provenance, where each key's value came from, and when the load finds
// them, the shadowed inputs and every source's inputs.
type loaded[T any] struct {
	cfg      *T
	files    fileDigests
	origins  map[string]Origin
	unknown  *UnknownKeysError
	shadowed []Shadow
	inputs   *inputs
}

// ended is why ctx ended, nil while it has not: ctx.Err(), with the cause
// [context.WithCancelCause] or a deadline's cause gave, so errors.Is reaches
// either.
func ended(ctx context.Context) error {
	err := ctx.Err()
	if err == nil {
		return nil
	}
	if cause := context.Cause(ctx); !errors.Is(cause, err) {
		return fmt.Errorf("%w: %w", err, cause)
	}
	return err
}

// abandoned is why a load whose context has ended stops: ended's error,
// named for the load. It is nil while ctx has not ended.
func abandoned(ctx context.Context) error {
	if err := ended(ctx); err != nil {
		return fmt.Errorf("loading config abandoned: %w", err)
	}
	return nil
}

// load is Load's pipeline. abandoned guards each boundary before a stage
// the caller supplies — a Defaulter, a provider, a file's parser, the
// environ function, decode hooks, the variable filter, a report callback,
// the secret populator, a post-load hook, the validator — so a load whose
// context has ended starts none of them, and one that ends inside such a
// stage fails as abandoned.
func load[T any](ctx context.Context, o *options) (loaded[T], error) {
	none := loaded[T]{}
	if err := abandoned(ctx); err != nil {
		return none, err
	}
	root := reflect.TypeFor[T]()
	if err := o.checkHookTypes(reflect.TypeFor[*T]()); err != nil {
		return none, err
	}
	schema, err := loadSchema(root, o)
	if err != nil {
		return none, fmt.Errorf("loading config: %w", err)
	}
	// A fresh instance per load: nothing from an earlier load (a key since
	// deleted from a file, say) can survive into this one.
	l := &loader{k: koanf.New("."), root: root, schema: schema}
	var rec *originRecorder
	switch {
	case o.provenance:
		rec = newOriginRecorder(schema)
		l.origins, l.provenance = rec.tree, true
	case o.findsShadowed(), o.claimsUnknown():
		l.origins = newOriginRoot()
	}
	if o.findsShadowed() {
		l.inputs = newInputs(schema, l.origins, o.inputDigests)
	}
	files := fileDigests{}
	var env envSchema
	var unmatchedEnv []string

	if _, err := l.addSchemaDefaults(o); err != nil {
		return none, err
	}
	if len(o.defaults) > 0 {
		if err := l.addFlat("WithDefaults", o.defaults, constant(Source{Kind: SourceDefaults})); err != nil {
			return none, err
		}
	}
	if err := abandoned(ctx); err != nil {
		return none, err
	}
	// Every source is read before any merges (see layers.go).
	var layers []*layer
	for i, src := range o.base {
		if err := abandoned(ctx); err != nil {
			return none, err
		}
		y, err := l.readProvider(ctx, fmt.Sprintf("base provider %d", i), src, positionSource(SourceBase, i))
		if err != nil {
			return none, err
		}
		layers = append(layers, y)
	}
	for _, f := range o.files {
		if err := abandoned(ctx); err != nil {
			return none, err
		}
		raw, err := os.ReadFile(f.path)
		switch {
		case err == nil:
		case !f.required && errors.Is(err, fs.ErrNotExist):
			continue // absence is the one failure an optional file is allowed
		default:
			return none, fmt.Errorf("loading config file %s: %w: %w", f.path, ErrFileRead, err)
		}
		data, err := parse(f.path, o.fileParser(f.path), raw)
		if err != nil {
			return none, fmt.Errorf("loading config file %s: %w: %w", f.path, ErrFileParse, err)
		}
		y, err := l.prepare("config file "+f.path, data, constant(Source{Kind: SourceFile, Name: f.path}))
		if err != nil {
			return none, err
		}
		layers = append(layers, y)
		files[f.path] = sha256.Sum256(raw)
	}
	if err := abandoned(ctx); err != nil {
		return none, err
	}
	if len(o.envPrefixes) > 0 {
		// Named from T's fields so a key with an underscore in it
		// (koanf:"max_conns") keeps it; see envSchema.
		env = newEnvSchema(variableFields(root), o.envPrefixes)
		values, from, unmatched, err := env.read(o.environ())
		if err != nil {
			return none, fmt.Errorf("loading env vars: %w: %w", ErrValidation, err)
		}
		unmatchedEnv = unmatched
		y, err := l.prepareFlat("env "+env.pattern(), values, envLabeler(env.pattern(), from))
		if err != nil {
			return none, err
		}
		layers = append(layers, y)
	}
	for i, src := range o.overrides {
		if err := abandoned(ctx); err != nil {
			return none, err
		}
		y, err := l.readProvider(ctx, fmt.Sprintf("override provider %d", i), src, positionSource(SourceOverride, i))
		if err != nil {
			return none, err
		}
		layers = append(layers, y)
	}
	if err := abandoned(ctx); err != nil {
		return none, err
	}
	verdicts, err := l.judgeEntries(ctx, o, layers)
	if err != nil {
		return none, err
	}
	// The origins record the defaults only with provenance, so a source's
	// claim on an unknown key looks for them here.
	var defaults map[string]any
	if o.claimsUnknown() {
		defaults = l.k.Raw()
	}
	if err := l.mergeLayers(ctx, layers, verdicts); err != nil {
		return none, err
	}
	o.log().DebugContext(ctx, "config sources loaded", "sources", l.sources)
	if len(l.aliased) > 0 {
		o.log().WarnContext(ctx, "config keys set under a former name; rename them", "keys", l.aliased)
	}
	var shadowed []Shadow
	if l.inputs != nil {
		if err := abandoned(ctx); err != nil {
			return none, err
		}
		if shadowed, err = o.judgeShadowed(ctx, root, l.inputs); err != nil {
			return none, err
		}
	}
	if err := abandoned(ctx); err != nil {
		return none, err
	}

	merged := l.k.Raw()
	elements := newElementDefaults(o, rec != nil)
	if err := elements.fill(root, merged); err != nil {
		return none, err
	}
	if rec != nil {
		rec.values = rec.tree.fill(merged, elements)
	}
	cfg := new(T)
	// The decoder's own record of the keys it left unused is what strict
	// keys checks, so strictness follows decoding exactly: squash, remain,
	// custom decoders, decode hooks and weak typing included.
	var observe func(mapstructure.DecodeHookFunc) mapstructure.DecodeHookFunc
	if rec != nil {
		observe = rec.observeHook
	}
	md, err := o.decodeInput(schema, merged, cfg, observe)
	if err != nil {
		return none, fmt.Errorf("unmarshaling config: %w: %w", ErrValidation, err)
	}
	l.unkeyed.apply(schema, reflect.ValueOf(cfg).Elem())
	if err := abandoned(ctx); err != nil {
		return none, err
	}
	if len(unmatchedEnv) > 0 {
		references, err := referencedVariables(schema, reflect.ValueOf(cfg))
		if err != nil {
			return none, err
		}
		unmatchedEnv = slices.DeleteFunc(unmatchedEnv, func(name string) bool {
			return references[name] || o.knownVariables[name] || o.variableFilter != nil && o.variableFilter(name)
		})
		if len(unmatchedEnv) > 0 {
			o.log().DebugContext(ctx, "environment variables under the prefix name no config key", "names", unmatchedEnv)
		}
	}
	if err := abandoned(ctx); err != nil {
		return none, err
	}
	claimed, unused, additional := l.claimUnknown(merged, defaults, md.Unused, layers)
	unknown, err := o.unknownInput(ctx, root, schema, merged, unused, env, unmatchedEnv, additional)
	if err != nil {
		return none, fmt.Errorf("unmarshaling config: %w: %w", ErrValidation, err)
	}
	accepted, err := reportUnknown(ctx, layers, claimed)
	if err != nil {
		return none, err
	}
	unknown = unknown.withKeys(root, accepted)

	// stage runs one stage that may change the decoded config, recording
	// what it changed when provenance is on.
	cv := reflect.ValueOf(cfg).Elem()
	stage := func(src Source, run func() error) error {
		if err := abandoned(ctx); err != nil {
			return err
		}
		if rec == nil {
			return run()
		}
		return rec.stage(cv, src, run)
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

	if err := o.validateConfig(ctx, cfg); err != nil {
		if stopped := abandoned(ctx); stopped != nil {
			return none, stopped
		}
		return none, err
	}

	out := loaded[T]{cfg: cfg, files: files, unknown: unknown, shadowed: shadowed, inputs: l.inputs}
	if rec != nil {
		if out.origins, err = rec.origins(cv); err != nil {
			return none, fmt.Errorf("recording provenance: %w", err)
		}
	}
	return out, nil
}

func (o *options) validateConfig(ctx context.Context, cfg any) error {
	if err := ended(ctx); err != nil {
		return err
	}
	if !o.skipValid {
		var err error
		if o.validator == nil {
			if err = refuseCycles(cfg); err == nil {
				err = validateDefault(ctx, cfg)
				if errors.Is(err, ErrSchema) {
					return err
				}
			}
		} else {
			err = validate(ctx, o.validator, cfg)
		}
		if err != nil {
			// The validator's error is kept whole as the cause, so a
			// wrapper's own sentinels and types stay reachable; go-playground
			// field errors found inside it only shape the message.
			summary := err.Error()
			var ve validator.ValidationErrors
			if errors.As(err, &ve) {
				summary = fieldErrorSummary(reflect.ValueOf(cfg), ve)
			}
			return &validationError{
				msg:   fmt.Sprintf("validating config: %s: %s", ErrValidation, summary),
				cause: err,
			}
		}
		if v, ok := any(cfg).(Validatable); ok {
			if err := v.Validate(); err != nil {
				return &validationError{
					msg:   fmt.Sprintf("validating config: %s: %s", ErrValidation, err),
					cause: err,
				}
			}
		}
	}

	return ended(ctx)
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

// validatorPackage prefixes the name of every function go-playground's
// validator defines.
const validatorPackage = "github.com/go-playground/validator/v10."

// validateDefault validates cfg with the default validator. The validator
// panics on a rule it cannot apply — min=abc, len or oneof on a kind they
// do not measure, required_if with an odd number of params, a dive or an
// unreadable tag in a struct behind a pointer, a collection or an
// interface, which [defaultRules] leaves to the value — wherever validation
// meets it. Such a panic is the declaration's fault, not the value's, and
// is returned as an ErrSchema naming the field the validator was
// validating, by key, and carrying the validator's message. A panic raised
// in the caller's own code, such as a method a validateFn rule calls, is
// left to propagate.
func validateDefault(ctx context.Context, cfg any) (err error) {
	// The validator calls a filter with each field's namespace as it enters
	// the field, and its own panics seldom name one: the last namespace is
	// where it was.
	var at []byte
	enter := func(ns []byte) bool {
		at = append(at[:0], ns...)
		return false
	}
	returned := false
	defer func() {
		if returned || !raisedByValidator() {
			return
		}
		r := recover()
		root := reflect.ValueOf(cfg)
		var where string
		if len(at) > 0 {
			schema, _ := rootSchema(root.Type())
			where = " at " + validationKey(root, schema, string(at), string(at))
		}
		err = fmt.Errorf("validating config: %w: a validate tag of %s does not apply to its field%s: %s", ErrSchema, gowalker.TypeText(derefType(root.Type())), where, panicText(r))
	}()
	err = defaultValidator().StructFilteredCtx(ctx, cfg, enter)
	returned = true
	return err
}

// raisedByValidator reports whether the panic unwinding through its caller,
// a deferred function, was raised by go-playground's own code: the first
// frame beneath the deferred call that is neither the runtime's, which
// raises a fault's panic, nor reflect's, which panics for the validator
// when it reads a value as a kind it is not.
func raisedByValidator() bool {
	pcs := make([]uintptr, 32)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(3, pcs)])
	for {
		f, more := frames.Next()
		if !strings.HasPrefix(f.Function, "runtime.") && !strings.HasPrefix(f.Function, "reflect.") {
			return strings.HasPrefix(f.Function, validatorPackage)
		}
		if !more {
			return false
		}
	}
}

// panicText is the message of a panic the validator raised. The validator
// panics with text it formats from a rule, its params, a type and a field's
// name, and through reflect or the runtime with an error naming a kind; an
// error of any other kind could quote the operating system's view of a
// field's value, a path, and is named by its type alone.
func panicText(r any) string {
	var re runtime.Error
	var ve *reflect.ValueError
	switch v := r.(type) {
	case string:
		return v
	case error:
		if errors.As(v, &re) || errors.As(v, &ve) {
			return v.Error()
		}
	}
	return fmt.Sprintf("a %T", r)
}

// defaultRules refuses a validate tag the default validator would meet on
// every validation of a value of T and cannot read — an undefined rule,
// keys without dive, an empty rule — which it would otherwise panic on in
// the middle of a load. Each tag is parsed once per type, and no rule runs.
func defaultRules(t reflect.Type) error {
	e := schemaMetadata(t)
	if e.err != nil {
		return e.err
	}
	e.rules.Do(func() { e.rulesErr = unreadableRules(e.node.typ) })
	return e.rulesErr
}

// unreadableRules parses, with the default validator's own parser (v), the
// validate tag of every field v reads on every validation of a value of
// root, whatever the value holds: the fields of root and of each struct it
// holds by value, as go-playground descends to them —
//
//   - every exported or embedded field not tagged validate:"-", those
//     tagged koanf:"-" and the structs that decode themselves from text
//     included;
//   - into a field's struct, time.Time and types convertible to it aside,
//     once its rules pass: v reads every tag of a struct it meets, and goes
//     on into the fields' values unless the rules reaching the struct say
//     structonly; nostructlevel stops before it;
//   - through a dive into an array of one or more elements, which holds
//     them whatever the value: each element is walked as a field of the
//     element type with the rules after the dive.
//
// On each of those structs' fields whose values v walks, it also refuses a
// dive v cannot take on the field's type, which v panics on whenever it
// applies it (see [misappliedDive]).
//
// A struct v reaches only when a value puts it there is left to validation
// (see [validateDefault]): one behind a pointer, a map, a slice or an
// interface, one beneath an omitempty or omitzero rule, and one whose value
// v replaces with its own ValidatorValue. An array of no elements is never
// walked.
func unreadableRules(root reflect.Type) error {
	w := ruleWalk{read: map[reflect.Type]bool{}, walked: map[reflect.Type]bool{}}
	w.fields(root, true)
	return errors.Join(w.errs...)
}

// ruleWalk is one unreadableRules walk. read holds the structs whose tags
// it has read, and walked those whose fields' values it has walked: a
// type's tags do not depend on the path reaching it.
type ruleWalk struct {
	read   map[reflect.Type]bool
	walked map[reflect.Type]bool
	errs   []error
}

// value walks what a field of type t holds by value — a struct, or the
// elements a dive reaches in an array — which v meets on every validation
// unless rules, the field's rules, stop it first.
func (w *ruleWalk) value(t reflect.Type, rules []string) {
	if validatedAsOther(t) {
		return
	}
	for i, rule := range rules {
		switch stepOf(rule) {
		case stepStop, stepWhenSet:
			// Whether v reads on past omitempty or omitzero is the value's.
			return
		case stepStructOnly:
			if walksFields(t) {
				w.fields(t, false)
			}
			return
		case stepDive:
			// A slice, a map or a pointer may hold no element; an array
			// holds its length's worth whatever the value.
			if t.Kind() == reflect.Array && t.Len() > 0 {
				w.value(t.Elem(), rules[i+1:])
			}
			return
		}
	}
	if walksFields(t) {
		w.fields(t, true)
	}
}

// fields reads the validate tags of struct t's fields as v does on meeting
// t, and, when descend, walks the fields' values.
func (w *ruleWalk) fields(t reflect.Type, descend bool) {
	read := validatorFields(t)
	if !w.read[t] {
		w.read[t] = true
		w.errs = append(w.errs, read.problems...)
	}
	if !descend || w.walked[t] {
		return
	}
	w.walked[t] = true
	for _, f := range read.fields {
		if problem := misappliedDive(f.typ, f.rules); problem != "" {
			w.errs = append(w.errs, fmt.Errorf("%w: field %s of %s has a validate tag the validator cannot apply: %s", ErrSchema, f.name, gowalker.TypeText(t), problem))
			continue
		}
		w.value(f.typ, f.rules)
	}
}

// ruleStep is what go-playground does with a field's value at one of the
// field's rules, as its traverseField reads them in order.
type ruleStep int

const (
	// stepRun runs the rule and reads the next.
	stepRun ruleStep = iota
	// stepStop walks nothing beneath the value: nostructlevel, endkeys.
	stepStop
	// stepWhenSet reads on only when the value is set: omitempty, omitzero.
	// A value they stop at holds no pointer, map or slice element.
	stepWhenSet
	// stepStructOnly reads a struct's tags and walks none of its fields'
	// values.
	stepStructOnly
	// stepDive walks each element of a slice, an array or a map with the
	// rules after the dive, and ends the field's own rules.
	stepDive
)

func stepOf(rule string) ruleStep {
	switch rule {
	case "nostructlevel", "endkeys":
		return stepStop
	case "omitempty", "omitzero":
		return stepWhenSet
	case "structonly":
		return stepStructOnly
	case "dive":
		return stepDive
	}
	return stepRun
}

// walksFields reports whether v, meeting a value of type t once a field's
// rules are read, goes on into its fields: a struct, time.Time and the
// types convertible to it aside.
func walksFields(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && !t.ConvertibleTo(timeType)
}

// structRules is a struct type as the default validator reads it on first
// meeting a value of it: the fields it walks, in order, and the tags it
// cannot read, on any of which it panics before walking a field.
type structRules struct {
	fields   []readRules
	problems []error
}

// readRules is a field whose validate tag v reads, and its rules: name is
// its Go name and alt the validator's (see [FieldName]), as its namespaces
// spell them.
type readRules struct {
	index int
	name  string
	alt   string
	typ   reflect.Type
	rules []string
}

// validatorFields is struct type t as the default validator reads it, read
// once per type for the life of the process, as the validator's own cache
// keeps it.
func validatorFields(t reflect.Type) *structRules {
	if read, ok := readStructs.Load(t); ok {
		return read.(*structRules)
	}
	read, _ := readStructs.LoadOrStore(t, readStruct(t))
	return read.(*structRules)
}

var readStructs sync.Map // reflect.Type → *structRules

// misappliedDive is the dive in rules, a field's rules, that v cannot take
// on t, the field's type, through pointers as v follows them: a dive on a
// kind that is not a slice, an array or a map, or keys after a dive on a
// slice or an array. v panics on one whenever it applies it, so the tag is
// wrong whatever the field holds: a collection's dive is applied to each
// element it holds. It is "" when the dives apply, where a value decides
// what v walks — an interface, a type v validates as its ValidatorValue —
// and past a dive into an array of no elements, which v never applies.
func misappliedDive(t reflect.Type, rules []string) string {
	for i, rule := range rules {
		if validatedAsOther(t) {
			return ""
		}
		t = derefType(t)
		switch stepOf(rule) {
		case stepStop, stepStructOnly:
			return "" // v goes no further
		case stepDive:
		default:
			continue
		}
		after := rules[i+1:]
		keys := len(after) > 0 && after[0] == "keys"
		switch t.Kind() {
		case reflect.Interface:
			return ""
		case reflect.Slice, reflect.Array:
			if t.Kind() == reflect.Array && t.Len() == 0 {
				return ""
			}
			if keys {
				return fmt.Sprintf("keys after a dive on %s, which is not a map", gowalker.TypeText(t))
			}
			return misappliedDive(t.Elem(), after)
		case reflect.Map:
			if !keys {
				return misappliedDive(t.Elem(), after)
			}
			// The keys' rules run to endkeys or to the tag's end; the rest
			// are the values'.
			end := slices.Index(after, "endkeys")
			if end < 0 {
				end = len(after)
			}
			if problem := misappliedDive(t.Key(), after[1:end]); problem != "" || end == len(after) {
				return problem
			}
			return misappliedDive(t.Elem(), after[end+1:])
		default:
			return fmt.Sprintf("dive on %s, which is not a slice, an array or a map", gowalker.TypeText(t))
		}
	}
	return ""
}

// validatedAsOther reports whether v validates a value of t, or a value t
// points to, as the ValidatorValue it returns rather than as itself.
func validatedAsOther(t reflect.Type) bool {
	base := derefType(t)
	for {
		if t.Implements(validatorValuer) {
			return true
		}
		if t == base {
			return false
		}
		t = t.Elem()
	}
}

// readStruct reads the validate tag of each field of struct t that the
// default validator reads, recording each tag it cannot read. The fields
// it returns are those whose tags read.
func readStruct(t reflect.Type) *structRules {
	read := &structRules{}
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("validate")
		if !f.IsExported() && !f.Anonymous || tag == "-" {
			continue
		}
		var rules []string
		if tag != "" {
			if problem := unreadableRule(defaultValidator(), tag); problem != "" {
				// The validator's complaint names the rule by the part before
				// '=', and the field; a tag holds the source's own text, never
				// a field's value.
				read.problems = append(read.problems, fmt.Errorf("%w: field %s of %s has a validate tag the validator cannot read: %s", ErrSchema, f.Name, gowalker.TypeText(t), problem))
				continue
			}
			rules = strings.Split(tag, ",")
		}
		read.fields = append(read.fields, readRules{index: i, name: f.Name, alt: FieldName(f), typ: f.Type, rules: rules})
	}
	return read
}

var (
	// timeType is time.Time, whose fields v never reads, nor those of a
	// type convertible to it.
	timeType = reflect.TypeFor[time.Time]()
	// validatorValuer is a type whose value v validates in place of its own.
	validatorValuer = reflect.TypeFor[validator.Valuer]()
)

// unreadableRule parses tag as v does when it first meets a struct, and
// returns what v panicked with when it could not. Validating a nil value
// reaches no rule, so nothing but the parse runs.
func unreadableRule(v *validator.Validate, tag string) (problem string) {
	defer func() {
		if r := recover(); r != nil {
			problem = strings.TrimSuffix(fmt.Sprint(r), " on field ''")
		}
	}()
	_ = v.Var(nil, tag)
	return ""
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

// fieldErrorSummary builds a compact summary of validation field errors
// for inclusion in the error message (e.g., "name(required), db.port(min=1)"),
// naming each field by its key in cfg, the validated config.
func fieldErrorSummary(cfg reflect.Value, ve validator.ValidationErrors) string {
	parts := make([]string, 0, len(ve))
	root, _ := rootSchema(cfg.Type())
	for _, fe := range ve {
		tag := fe.Tag()
		key := validationKey(cfg, root, fe.StructNamespace(), fe.Namespace())
		p, exact := validatorKey(cfg, fe.StructNamespace())
		showParam := false
		if root != nil {
			if c, n, err := root.walkTo(cursor{}, p); err == nil && exact {
				showParam = !c.sensitive() && !mayHoldSensitive(n.typ, map[reflect.Type]bool{})
			}
		}
		if fe.Param() != "" && showParam {
			if param, ok := keyedParam(cfg, root, fe); ok {
				tag += "=" + param
			}
		}
		parts = append(parts, key+"("+tag+")")
	}
	return strings.Join(parts, ", ")
}

// Cross-field rules name other fields of the struct holding the field, by Go
// name, in their params: each rule's field-naming params, by position.
var (
	fieldParamRules = map[string]bool{ // the param is one field
		"eqfield": true, "nefield": true, "gtfield": true, "gtefield": true, "ltfield": true, "ltefield": true,
		"eqcsfield": true, "necsfield": true, "gtcsfield": true, "gtecsfield": true, "ltcsfield": true, "ltecsfield": true,
		"fieldcontains": true, "fieldexcludes": true,
	}
	fieldValueParamRules = map[string]bool{ // field value pairs
		"required_if": true, "required_unless": true, "excluded_if": true, "excluded_unless": true, "skip_unless": true,
	}
	fieldListParamRules = map[string]bool{ // a list of fields
		"required_with": true, "required_with_all": true, "required_without": true, "required_without_all": true,
		"excluded_with": true, "excluded_with_all": true, "excluded_without": true, "excluded_without_all": true,
	}
	ruleParams = regexp.MustCompile(`'[^']*'|\S+`) // the validator's own split
)

// keyedParam is fe's param as a summary shows it: with every field it names
// replaced by that field's key, so gtefield=MinWait reads gtefield=min_wait.
// The second result is false when the param must be withheld: a rule
// comparing against a value of a sensitive field would reveal it. A field
// the param names that cannot be found keeps its Go name.
func keyedParam(cfg reflect.Value, root *schemaNode, fe validator.FieldError) (string, bool) {
	rule, param := fe.Tag(), fe.Param()
	if !fieldParamRules[rule] && !fieldValueParamRules[rule] && !fieldListParamRules[rule] {
		return param, true
	}
	// The fields a param names are the holding struct's; an unnamed root's
	// own fields have no namespace above them.
	var parent string
	if p, ok := parentNamespace(fe.StructNamespace()); ok {
		parent = p + "."
	}
	tokens := []string{param}
	if !fieldParamRules[rule] {
		tokens = ruleParams.FindAllString(param, -1)
	}
	for i, token := range tokens {
		if fieldValueParamRules[rule] && i%2 == 1 {
			continue // a value, not a field
		}
		p, exact := validatorKey(cfg, parent+token)
		if !exact || len(p) == 0 {
			continue
		}
		if fieldValueParamRules[rule] && root != nil {
			if c, n, err := root.walkTo(cursor{}, p); err != nil || c.sensitive() || mayHoldSensitive(n.typ, map[reflect.Type]bool{}) {
				return "", false
			}
		}
		tokens[i] = p.String()
	}
	return strings.Join(tokens, " "), true
}

// parentNamespace is the validator namespace of the struct holding the field
// at ns, Config.DB for Config.DB.Port, or Config for Config.Hosts[1].
func parentNamespace(ns string) (string, bool) {
	depth := 0
	for i := len(ns) - 1; i >= 0; i-- {
		switch ns[i] {
		case ']':
			depth++
		case '[':
			depth--
		case '.':
			if depth == 0 {
				return ns[:i], true
			}
		}
	}
	return "", false
}

// validationKey names the field of cfg at the validator's struct namespace
// structNS by its key, cut short above the keys of a sensitive container
// (see [diagnosticPath]). ns is the validator's own name for the field,
// named by when structNS reaches no key of cfg.
func validationKey(cfg reflect.Value, root *schemaNode, structNS, ns string) string {
	p, exact := validatorKey(cfg, structNS)
	if safe := diagnosticPath(root, p); len(safe) < len(p) {
		return safe.orRoot()
	}
	switch {
	case exact:
		return p.orRoot()
	case len(p) > 0:
		return p.String() + " (exact location unavailable)"
	}
	// Not a namespace of cfg: the validator's own, past the root's part.
	if rest, ok := belowValidatorRoot(cfg.Type(), ns); ok {
		return rest
	}
	return ns
}
