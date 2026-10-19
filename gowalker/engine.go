package gowalker

import (
	"reflect"
	"sync"
)

// Limits bound one compilation or resolution. All fields must be positive.
// MaxTypes counts unique declared types. MaxFields counts declared fields plus
// visits made while expanding named views (including inline fields). MaxDepth
// counts first-visit type edges from the root, whose depth is zero; it also caps
// inline expansion depth and consecutive pointer/interface hops during access.
// MaxPathSteps counts explicit path steps. Pointer following is cycle checked.
type Limits struct {
	MaxTypes     int
	MaxFields    int
	MaxDepth     int
	MaxPathSteps int
}

// DefaultLimits returns a copy of the initial conservative resource budgets.
func DefaultLimits() Limits {
	return Limits{MaxTypes: 4096, MaxFields: 65536, MaxDepth: 128, MaxPathSteps: 256}
}

type settings struct {
	resolver FieldResolver
	tagged   *tagNaming
	atomic   []func(reflect.Type) bool
	fold     bool
	capacity int
	limits   Limits
}

// rule applies the engine's naming convention to one declared field.
func (s *settings) rule(sf reflect.StructField) (FieldRule, error) {
	switch {
	case s.tagged != nil:
		return s.tagged.rule(sf, s.isAtomic)
	case s.resolver != nil:
		return s.resolver(copyStructField(sf))
	}
	return FieldRule{Name: sf.Name}, nil
}

func (s *settings) isAtomic(t reflect.Type) bool {
	for _, classify := range s.atomic {
		if classify(t) {
			return true
		}
	}
	return false
}

// Option configures a new immutable engine. Callbacks must be deterministic and
// concurrency safe; changing captured state does not reconfigure an engine.
type Option func(*settings) error

// WithFieldResolver replaces the default naming rule; the last WithFieldResolver
// or [WithTaggedFields] option applies. Visibility checks still apply after the
// resolver runs; it cannot expose a private field directly.
func WithFieldResolver(resolver FieldResolver) Option {
	return func(s *settings) error {
		if resolver == nil {
			return ErrSchema
		}
		s.resolver, s.tagged = resolver, nil
		return nil
	}
}

// WithAtomicType marks types that named paths must treat as terminal values.
// The callback receives exact declared types, including pointer types. Leaf
// rules accumulate: a type is a leaf when any WithAtomicType or
// [WithAtomicImplementers] option reports it.
func WithAtomicType(classify func(reflect.Type) bool) Option {
	return func(s *settings) error {
		if classify == nil {
			return ErrSchema
		}
		s.atomic = append(s.atomic, classify)
		return nil
	}
}

// WithAtomicImplementers makes a leaf of every type that implements one of
// ifaces through its value or its pointer: types that decode, scan or render
// themselves, such as encoding.TextUnmarshaler (time.Time, netip.Addr) or
// sql.Scanner. Each element of ifaces must be an interface type.
func WithAtomicImplementers(ifaces ...reflect.Type) Option {
	return func(s *settings) error {
		if len(ifaces) == 0 {
			return ErrSchema
		}
		for _, iface := range ifaces {
			if iface == nil || iface.Kind() != reflect.Interface {
				return ErrSchema
			}
		}
		ifaces = append([]reflect.Type(nil), ifaces...)
		s.atomic = append(s.atomic, func(t reflect.Type) bool {
			for _, iface := range ifaces {
				if t.Implements(iface) || t.Kind() != reflect.Interface && reflect.PointerTo(t).Implements(iface) {
					return true
				}
			}
			return false
		})
		return nil
	}
}

// WithCaseFoldNames uses Unicode simple folding for both lookup and collisions.
func WithCaseFoldNames() Option {
	return func(s *settings) error { s.fold = true; return nil }
}

// WithCacheCapacity limits retained root schemas, including each schema's graph.
// Zero disables retention. At capacity, new types compile without being cached.
func WithCacheCapacity(capacity int) Option {
	return func(s *settings) error {
		if capacity < 0 {
			return ErrSchema
		}
		s.capacity = capacity
		return nil
	}
}

// WithLimits replaces all compile/access budgets. Start from DefaultLimits to
// adjust individual budgets. Zero and negative values are rejected.
func WithLimits(limits Limits) Option {
	return func(s *settings) error {
		if limits.MaxTypes < 1 || limits.MaxFields < 1 || limits.MaxDepth < 1 || limits.MaxPathSteps < 1 {
			return ErrSchema
		}
		s.limits = limits
		return nil
	}
}

// Engine owns a field policy and a bounded cache. Use New to construct one.
// It must not be copied after first use. Compiling does not retain caller values.
type Engine struct {
	settings settings
	mu       sync.RWMutex
	cache    map[reflect.Type]*Schema
}

// New creates an engine using exported Go field names, exact matching, a cache
// capacity of 256 root schemas, and DefaultLimits unless options override them.
func New(options ...Option) (*Engine, error) {
	s := settings{capacity: 256, limits: DefaultLimits()}
	for _, option := range options {
		if option == nil {
			return nil, failure("new", ErrSchema, nil, emptyPath, -1, nil)
		}
		if err := option(&s); err != nil {
			return nil, failure("new", ErrSchema, nil, emptyPath, -1, err)
		}
	}
	return &Engine{settings: s, cache: make(map[reflect.Type]*Schema)}, nil
}

// defaultEngine backs the package-level functions. It is unexported so that no
// library can change the field rules every other user of the package relies on.
var defaultEngine = MustNew()

// Default returns the process-wide engine behind [Compile], [CompileType],
// [Get], [GetAs], [Set], [WalkType] and [WalkValue]: exported Go field names,
// exact matching, [DefaultLimits] and a cache of 256 root schemas. A library
// with its own naming rules creates its own engine with [MustNew] or [New].
func Default() *Engine { return defaultEngine }

// Compile compiles T with the [Default] engine, reusing its cached schema.
func Compile[T any]() (*Schema, error) { return defaultEngine.Compile[T]() }

// CompileType compiles t with the [Default] engine, reusing its cached schema.
func CompileType(t reflect.Type) (*Schema, error) { return defaultEngine.CompileType(t) }

// MustNew is New for a package-level engine whose options are fixed in source.
// It panics if an option is invalid.
//
//	var columns = gowalker.MustNew(gowalker.WithTaggedFields("db"))
func MustNew(options ...Option) *Engine {
	e, err := New(options...)
	if err != nil {
		panic(err)
	}
	return e
}

// Compile compiles T without needing an instance, including an interface T.
func (e *Engine) Compile[T any]() (*Schema, error) {
	return e.CompileType(reflect.TypeFor[T]())
}

// CompileType returns a fully constructed immutable schema for a runtime type.
// Concurrent cold requests
// may build independently; only complete results are published under the lock.
// Recursive construction never waits on an unfinished cache entry.
func (e *Engine) CompileType(t reflect.Type) (*Schema, error) {
	if e == nil || e.cache == nil || t == nil {
		return nil, failure("compile", ErrType, t, emptyPath, -1, nil)
	}
	e.mu.RLock()
	cached := e.cache[t]
	e.mu.RUnlock()
	if cached != nil {
		return cached, nil
	}
	b := builder{engine: e, nodes: make(map[reflect.Type]*Shape)}
	root, err := b.build(t, 0)
	if err != nil {
		return nil, err
	}
	if err := b.view(root); err != nil {
		return nil, err
	}
	schema := &Schema{engine: e, root: root}
	e.mu.Lock()
	defer e.mu.Unlock()
	if cached := e.cache[t]; cached != nil {
		return cached, nil
	}
	if len(e.cache) < e.settings.capacity {
		e.cache[t] = schema
	}
	return schema, nil
}
