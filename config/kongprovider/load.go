package kongprovider

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"github.com/alecthomas/kong"

	"github.com/ashtonian/pkg/config"
)

// Load returns a Kong option that makes cfg, a config struct inside the CLI
// struct, the loaded config for the command that holds it. It describes
// cfg's flags in help as [HelpFromConfig] does. When the selected command
// path holds a flag that writes into cfg, Load runs config.Load for T
// during Parse, before Kong validates anything: the options opts returns,
// the environment under envPrefixes, and the flags typed on the command line
// above both. It writes the result into *cfg and binds cfg, so a Run method
// can take it as a *T parameter or read it from its receiver.
//
//	kctx := kong.Parse(&cli, kongprovider.Load(ctx, &cli.Serve.AppConfig, []string{"APP"},
//	    func(*kong.Context) ([]config.Option, error) {
//	        return []config.Option{config.WithFile(cli.Config)}, nil
//	    }))
//	kctx.FatalIfErrorf(kctx.Run())
//
// After Parse, every field of *cfg holds the loaded value — fields with no
// flag (`kong:"-"`) included — and the Validate methods Kong runs on the
// command, cfg and cfg's embedded sections see that value, never Kong's
// defaults and typed flags alone. config.Load runs cfg's own Validate, and
// its validate tags, as part of the load.
//
// opts runs once, after Kong has parsed the command line and before the load,
// so it can read the CLI's own flags and arguments, such as a config file's
// path. It may be nil. envPrefixes is the load's whole environment layer, as
// config.WithEnvPrefixes takes it, and the variables the help names: an
// environment option among opts is overridden, and with no prefixes the
// environment is not read. Do not also pass HelpFromConfig or
// [WithOverridesFromStruct] for cfg; Load does both.
//
// ctx bounds the load. A failed load fails Parse with the load's error,
// which keeps the config package's categories (config.ErrValidation, …) and
// exits 1 under kong.Parse, never Kong's usage-error status; Kong prints its
// usage with it only when the parser was built with kong.UsageOnError. A
// command whose path holds none of cfg's flags loads nothing, so a sibling
// command — one that prints a starter file, say — runs without a valid
// config. A CLI none of whose flags writes into cfg fails when Kong builds
// the parser.
func Load[T any](ctx context.Context, cfg *T, envPrefixes []string, opts func(*kong.Context) ([]config.Option, error)) kong.Option {
	return wire(cfg, envPrefixes, opts, func(kctx *kong.Context, loadOpts []config.Option) (*T, error) {
		loaded, err := config.Load[T](ctx, loadOpts...)
		if err != nil {
			return nil, err
		}
		kctx.Bind(cfg)
		return loaded, nil
	})
}

// LoadReloader is [Load] for a service that reloads. From the same sources,
// it builds a config.Reloader[T] with reloadOpts, writes a copy of the
// reloader's first snapshot into *cfg, and binds the *config.Reloader[T]
// for Run methods. It binds no *T: code reads r.Config() wherever it needs
// the live value. *cfg is the startup config — what keys that wait for a
// restart hold for the life of the process — and never changes after Parse.
//
// LoadReloader does not arm, run or stop the reloader; that stays with the
// command:
//
//	func (c *ServeCmd) Run(r *config.Reloader[AppConfig]) error {
//	    if err := r.Arm(ctx); err != nil {
//	        return err
//	    }
//	    go r.Run(ctx)
//	    // ... serve, reading r.Config() for live keys ...
//	    stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
//	    defer cancel()
//	    return r.Stop(stopCtx)
//	}
//
// ctx bounds the initial load only, and the ApplyInitial appliers among
// reloadOpts run during Parse; reloads use the context passed to Run or
// Reload. The typed flags are copied when the reloader is built, so every
// reload keeps the startup command line: a flag pins its key for the life of
// the process.
func LoadReloader[T any](ctx context.Context, cfg *T, envPrefixes []string, opts func(*kong.Context) ([]config.Option, error), reloadOpts ...config.ReloadOption) kong.Option {
	reloadOpts = slices.Clone(reloadOpts)
	return wire(cfg, envPrefixes, opts, func(kctx *kong.Context, loadOpts []config.Option) (*T, error) {
		r, err := config.NewReloader[T](ctx, loadOpts, reloadOpts...)
		if err != nil {
			return nil, err
		}
		kctx.Bind(r)
		// Published configs are shared and immutable; *cfg is the caller's.
		startup := copyValue(reflect.ValueOf(r.Config()).Elem()).(T)
		return &startup, nil
	})
}

// wire is Load and LoadReloader without the load itself: the help, the
// check that the CLI writes into cfg, and the hook that loads during Parse.
func wire[T any](cfg *T, envPrefixes []string, opts func(*kong.Context) ([]config.Option, error), load func(*kong.Context, []config.Option) (*T, error)) kong.Option {
	envPrefixes = slices.Clone(envPrefixes)
	return kong.OptionFunc(func(k *kong.Kong) error {
		if cfg == nil {
			return errors.New("kongprovider: nil config")
		}
		build := kong.PostBuild(func(k *kong.Kong) error {
			fields, err := describeFlags(k, cfg, envPrefixes)
			if err != nil {
				return err
			}
			if len(fields) == 0 {
				return errNoFlag(k.Model.Name)
			}
			return nil
		})
		// Kong calls an option's hook once for every element of the parsed
		// path. The last element's call comes after every BeforeApply method
		// the CLI's own types declare, so those still see Kong's semantics:
		// values not yet applied.
		hook := kong.WithBeforeApply(func(kctx *kong.Context, at *kong.Path) error {
			if at != kctx.Path[len(kctx.Path)-1] {
				return nil
			}
			return loadInto(kctx, cfg, envPrefixes, opts, load)
		})
		for _, o := range []kong.Option{build, hook} {
			if err := o.Apply(k); err != nil {
				return err
			}
		}
		return nil
	})
}

// loadInto loads T for the parse kctx records, when its command path holds
// one of cfg's flags, and leaves Kong to apply the loaded values.
//
// Kong's Parse runs Reset, Resolve, the BeforeApply hooks, Apply, Validate,
// then AfterApply, and offers no hook between Apply and Validate; loading
// any later would let Kong validate its own defaults and typed flags. So the
// load runs here, and the parsed values of cfg's flags are replaced with the
// loaded ones: Kong's Apply then writes the loaded config, and its
// validators see it.
func loadInto[T any](kctx *kong.Context, cfg *T, envPrefixes []string, opts func(*kong.Context) ([]config.Option, error), load func(*kong.Context, []config.Option) (*T, error)) error {
	fields, err := fieldsOfFlags(cfg, allFlags(kctx.Model.Node))
	if err != nil {
		return err
	}
	if !selected(kctx.Path, fields) {
		return nil
	}
	// Applying what Kong parsed now, which its Apply repeats, gives opts the
	// CLI's own values and WithOverridesFromStruct the typed flags'.
	if _, err := kctx.Apply(); err != nil {
		return err
	}
	var loadOpts []config.Option
	if opts != nil {
		if loadOpts, err = opts(kctx); err != nil {
			return err
		}
	}
	loadOpts = append(slices.Clip(loadOpts),
		config.WithEnvPrefixes(envPrefixes...),
		WithOverridesFromStruct(kctx, cfg),
	)
	loaded, err := load(kctx, loadOpts)
	if err != nil {
		return err
	}
	a := assigner{targets: flagTargets(fields), seen: map[uintptr]bool{}}
	a.assign(reflect.ValueOf(cfg).Elem(), reflect.ValueOf(loaded).Elem())
	for _, el := range kctx.Path {
		if el.Flag == nil {
			continue
		}
		if _, ok := fields[el.Flag]; !ok {
			continue
		}
		parsed := kctx.Value(el)
		if !parsed.CanSet() {
			return fmt.Errorf("kongprovider: Kong's parsed value of --%s cannot be replaced with the loaded one; this Kong version is not supported", el.Flag.Name)
		}
		parsed.Set(el.Flag.Target)
	}
	return nil
}

// selected reports whether a node on path — the application, a command, an
// argument — holds a flag that writes into the config.
func selected(path []*kong.Path, fields map[*kong.Flag]config.Field) bool {
	for _, el := range path {
		n := el.Node()
		if n == nil {
			continue
		}
		for _, fl := range n.Flags {
			if _, ok := fields[fl]; ok {
				return true
			}
		}
	}
	return false
}

// flagTargets returns the address of every field a flag of the config
// writes.
func flagTargets(fields map[*kong.Flag]config.Field) []uintptr {
	targets := make([]uintptr, 0, len(fields))
	for fl := range fields {
		targets = append(targets, fl.Target.UnsafeAddr())
	}
	return targets
}

// assigner writes a loaded config into the CLI's. Kong allocates the struct
// an embedded pointer section points at, and each flag writes through the
// address it was built with, so a pointer leading to a flag's field is kept
// and the loaded section written into what it points at; replacing it would
// detach those flags from the config. A section the load left nil is set
// nil, so the config equals the loaded one.
type assigner struct {
	targets []uintptr
	seen    map[uintptr]bool // pointed-at sections already written
}

func (a *assigner) assign(dst, src reflect.Value) {
	switch {
	case dst.Kind() == reflect.Pointer && !src.IsNil() && a.leads(dst, map[uintptr]bool{}):
		if a.seen[dst.Pointer()] {
			return
		}
		a.seen[dst.Pointer()] = true
		a.assign(dst.Elem(), src.Elem())
	case dst.Kind() == reflect.Struct && (!dst.CanSet() || a.leads(dst, map[uintptr]bool{})):
		// Field by field: an unexported embedded struct cannot be set whole,
		// though its exported fields can.
		for i := range dst.NumField() {
			a.assign(dst.Field(i), src.Field(i))
		}
	case dst.CanSet():
		dst.Set(src)
	}
}

// leads reports whether v, an addressable struct or a pointer to one, holds
// a flag's field or reaches one through the struct pointers it holds.
func (a *assigner) leads(v reflect.Value, visiting map[uintptr]bool) bool {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() || v.Type().Elem().Kind() != reflect.Struct || visiting[v.Pointer()] {
			return false
		}
		visiting[v.Pointer()] = true
		return a.leads(v.Elem(), visiting)
	case reflect.Struct:
		addr, size := v.UnsafeAddr(), v.Type().Size()
		if slices.ContainsFunc(a.targets, func(t uintptr) bool { return t >= addr && t < addr+size }) {
			return true
		}
		// A nested struct lies within v's bytes; only pointers lead outside.
		for i := range v.NumField() {
			if f := v.Field(i); f.Kind() == reflect.Pointer && a.leads(f, visiting) {
				return true
			}
		}
	}
	return false
}

func errNoFlag(cli string) error {
	return fmt.Errorf("kongprovider: no flag of the CLI %s writes a field of the supplied config; pass a pointer into the parsed CLI struct", cli)
}
