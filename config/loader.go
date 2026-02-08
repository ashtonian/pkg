package config

import (
	"context"
	"encoding"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
)

// loaderOptions holds all configuration for a Loader.
type loaderOptions struct {
	envPrefix       string
	configFiles     []configFileEntry
	defaults        map[string]any
	secretProvider  SecretProvider
	secretCtx       context.Context
	skipValidation  bool
	customValidator *validator.Validate
}

type configFileEntry struct {
	path     string
	required bool
}

// Loader loads, validates, and manages configuration of type T.
type Loader[T any] struct {
	config  *T
	options loaderOptions
	fields  []fieldMeta
	values  map[string]string
}

// NewLoader creates a new Loader with the given options.
func NewLoader[T any](opts ...Option) (*Loader[T], error) {
	var options loaderOptions
	for _, opt := range opts {
		opt(&options)
	}

	var zero T
	t := reflect.TypeOf(zero)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("config type must be a struct, got %s", t.Kind())
	}

	fields := parseStructFields(t, "", nil)

	// Apply env prefix to auto-derived names.
	if options.envPrefix != "" {
		prefix := strings.TrimSuffix(options.envPrefix, "_") + "_"
		for i := range fields {
			// Only prefix auto-derived names (those that don't already contain the prefix).
			if !strings.HasPrefix(fields[i].EnvName, prefix) {
				fields[i].EnvName = prefix + fields[i].EnvName
			}
			fields[i].FileKey = toSnakeCase(fields[i].EnvName)
		}
	}

	return &Loader[T]{
		options: options,
		fields:  fields,
		values:  make(map[string]string),
	}, nil
}

// Load resolves configuration from all sources and returns the populated config.
// Source precedence: env > files > programmatic defaults > struct tag defaults.
func (l *Loader[T]) Load() (*T, error) {
	// Layer 1: Apply struct tag defaults.
	for _, f := range l.fields {
		if f.HasDefault {
			l.values[f.EnvName] = f.DefaultValue
		}
	}

	// Layer 2: Apply programmatic defaults.
	for key, val := range l.options.defaults {
		l.values[key] = fmt.Sprintf("%v", val)
	}

	// Layer 3: Load from config files (merged left-to-right).
	for _, cf := range l.options.configFiles {
		src := NewFileSource(cf.path, cf.required)
		fileVals, err := src.Load()
		if err != nil {
			return nil, err
		}
		l.applyFileValues(fileVals)
	}

	// Layer 4: Load from environment variables (highest precedence).
	envSrc := NewEnvSource("")
	envVals, err := envSrc.Load()
	if err != nil {
		return nil, fmt.Errorf("loading env vars: %w", err)
	}
	for _, f := range l.fields {
		if val, ok := envVals[f.EnvName]; ok {
			l.values[f.EnvName] = val
		}
	}

	// Layer 5: Apply secret provider values.
	if l.options.secretProvider != nil {
		if err := l.applySecrets(); err != nil {
			return nil, err
		}
	}

	// Build the config struct from resolved values.
	cfg, err := l.buildConfig()
	if err != nil {
		return nil, err
	}

	// Check required fields.
	if err := l.checkRequired(cfg); err != nil {
		return nil, err
	}

	// Validate.
	if !l.options.skipValidation {
		if err := l.Validate(cfg); err != nil {
			return nil, err
		}
	}

	l.config = cfg
	return cfg, nil
}

// applyFileValues maps file keys to field env names and applies values.
func (l *Loader[T]) applyFileValues(fileVals map[string]string) {
	if fileVals == nil {
		return
	}
	for _, f := range l.fields {
		if val, ok := fileVals[f.FileKey]; ok {
			l.values[f.EnvName] = val
		}
	}
}

// applySecrets fetches values from the secret provider for fields with secret tags.
func (l *Loader[T]) applySecrets() error {
	ctx := l.options.secretCtx
	if ctx == nil {
		ctx = context.Background()
	}
	for _, f := range l.fields {
		if f.SecretKey == "" {
			continue
		}
		val, err := l.options.secretProvider.GetSecret(ctx, f.SecretKey)
		if err != nil {
			return fmt.Errorf("fetching secret for field %s (key %q): %w", f.FieldName, f.SecretKey, ErrSecretProvider)
		}
		l.values[f.EnvName] = val
	}
	return nil
}

// buildConfig creates a new T and sets field values from the resolved map.
func (l *Loader[T]) buildConfig() (*T, error) {
	cfg := new(T)
	v := reflect.ValueOf(cfg).Elem()

	for _, f := range l.fields {
		val, ok := l.values[f.EnvName]
		if !ok || val == "" {
			continue
		}

		fv := v.FieldByIndex(f.FieldIndex)
		if !fv.CanSet() {
			continue
		}

		if err := setFieldValue(fv, val); err != nil {
			return nil, fmt.Errorf("setting field %s to %q: %w", f.FieldName, val, ErrUnsupportedType)
		}
	}
	return cfg, nil
}

// checkRequired verifies that all required fields have non-zero values.
func (l *Loader[T]) checkRequired(cfg *T) error {
	v := reflect.ValueOf(cfg).Elem()
	var missing []string
	for _, f := range l.fields {
		if !f.Required {
			continue
		}
		fv := v.FieldByIndex(f.FieldIndex)
		if fv.IsZero() {
			missing = append(missing, f.EnvName)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("required fields not set: %s: %w", strings.Join(missing, ", "), ErrRequired)
	}
	return nil
}

// Validate runs go-playground/validator rules on the loaded config.
func (l *Loader[T]) Validate(cfg *T) error {
	v := l.options.customValidator
	if v == nil {
		v = validator.New()
	}
	if err := v.Struct(cfg); err != nil {
		return fmt.Errorf("config validation: %w", ErrValidation)
	}
	return nil
}

// Print outputs the configuration in the requested format with sensitive fields redacted.
func (l *Loader[T]) Print(opts ...PrintOption) {
	printConfig(l.fields, l.values, opts...)
}

// ToMap returns the configuration as a map. If redact is true, sensitive fields are masked.
func (l *Loader[T]) ToMap(redact bool) map[string]any {
	return toMap(l.fields, l.values, redact)
}

// Fields returns metadata about all config fields.
func (l *Loader[T]) Fields() []fieldMeta {
	return l.fields
}

// setFieldValue converts a string value and sets it on the reflect.Value.
func setFieldValue(fv reflect.Value, val string) error {
	ft := fv.Type()

	// Handle encoding.TextUnmarshaler interface first (works for both pointer
	// and value receivers).
	if tu, ok := asTextUnmarshaler(fv); ok {
		if err := tu.UnmarshalText([]byte(val)); err != nil {
			return fmt.Errorf("unmarshaling text for %s: %w", ft, err)
		}
		return nil
	}

	// Handle time.Duration specially.
	if ft == reflect.TypeOf(time.Duration(0)) {
		d, err := time.ParseDuration(val)
		if err != nil {
			return fmt.Errorf("parsing duration %q: %w", val, err)
		}
		fv.Set(reflect.ValueOf(d))
		return nil
	}

	switch ft.Kind() {
	case reflect.Ptr:
		return setPointerField(fv, val)
	case reflect.Slice:
		return setSliceField(fv, val)
	case reflect.Map:
		return setMapField(fv, val)
	case reflect.String:
		fv.SetString(val)
	case reflect.Bool:
		b, err := strconv.ParseBool(val)
		if err != nil {
			return fmt.Errorf("parsing bool %q: %w", val, err)
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return fmt.Errorf("parsing int %q: %w", val, err)
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(val, 10, 64)
		if err != nil {
			return fmt.Errorf("parsing uint %q: %w", val, err)
		}
		fv.SetUint(n)
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return fmt.Errorf("parsing float %q: %w", val, err)
		}
		fv.SetFloat(n)
	default:
		return fmt.Errorf("unsupported type: %s", ft.Kind())
	}
	return nil
}

// setPointerField allocates the target pointer and recursively sets its value.
func setPointerField(fv reflect.Value, val string) error {
	elemType := fv.Type().Elem()
	ptr := reflect.New(elemType)
	if err := setFieldValue(ptr.Elem(), val); err != nil {
		return err
	}
	fv.Set(ptr)
	return nil
}

// setSliceField splits a comma-separated string and sets each element.
func setSliceField(fv reflect.Value, val string) error {
	elemType := fv.Type().Elem()
	parts := strings.Split(val, ",")
	slice := reflect.MakeSlice(fv.Type(), len(parts), len(parts))

	for i, part := range parts {
		part = strings.TrimSpace(part)
		elem := reflect.New(elemType).Elem()
		if err := setFieldValue(elem, part); err != nil {
			return fmt.Errorf("slice element %d %q: %w", i, part, ErrSliceParse)
		}
		slice.Index(i).Set(elem)
	}
	fv.Set(slice)
	return nil
}

// setMapField parses "key=val,key2=val2" format and sets the map field.
func setMapField(fv reflect.Value, val string) error {
	keyType := fv.Type().Key()
	valType := fv.Type().Elem()
	m := reflect.MakeMap(fv.Type())

	pairs := strings.Split(val, ",")
	for _, pair := range pairs {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		k, v, ok := strings.Cut(pair, "=")
		if !ok {
			return fmt.Errorf("map entry %q missing '=' separator: %w", pair, ErrMapParse)
		}

		keyVal := reflect.New(keyType).Elem()
		if err := setFieldValue(keyVal, strings.TrimSpace(k)); err != nil {
			return fmt.Errorf("map key %q: %w", k, ErrMapParse)
		}

		valVal := reflect.New(valType).Elem()
		if err := setFieldValue(valVal, strings.TrimSpace(v)); err != nil {
			return fmt.Errorf("map value %q for key %q: %w", v, k, ErrMapParse)
		}

		m.SetMapIndex(keyVal, valVal)
	}
	fv.Set(m)
	return nil
}

// asTextUnmarshaler checks if fv (or a pointer to it) implements encoding.TextUnmarshaler.
func asTextUnmarshaler(fv reflect.Value) (encoding.TextUnmarshaler, bool) {
	if fv.CanInterface() {
		if tu, ok := fv.Interface().(encoding.TextUnmarshaler); ok {
			return tu, true
		}
	}
	if fv.CanAddr() {
		if tu, ok := fv.Addr().Interface().(encoding.TextUnmarshaler); ok {
			return tu, true
		}
	}
	return nil, false
}
