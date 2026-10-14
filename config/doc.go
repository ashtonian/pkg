// Package config loads typed configuration for Go services from one tagged
// struct, on top of koanf. The struct is the schema: its koanf, alias, env,
// default, sep, format, example, validate, desc, sensitive and secret tags
// give each field its key, former names, variables, default, list separator,
// text format, example, rules, description and secrecy. Loading, validation, redaction, usage
// tables, starter files and the JSON Schema all read them. [Load] merges
// struct-tag defaults, files, environment variables and explicit overrides
// into a *T, then resolves secrets, normalizes and validates it in one
// pipeline; [Reloader] re-runs that pipeline on a file change, signal, poll
// tick or application notification and publishes a new config only when it
// passed.
//
// # Quick Start
//
//	type AppConfig struct {
//	    Port    int           `koanf:"port" default:"8080" validate:"min=1,max=65535" desc:"HTTP listen port"`
//	    Timeout time.Duration `koanf:"timeout" default:"30s"`
//	    DB      struct {
//	        MaxConns int    `koanf:"max_conns" default:"25"`
//	        Password string `koanf:"password" validate:"required"`
//	    } `koanf:"db"`
//	}
//
//	cfg, err := config.Load[AppConfig](ctx,
//	    config.WithFile("config.yaml"), // optional
//	    config.WithEnvPrefix("APP"),    // APP_PORT, APP_DB_MAX_CONNS, ...
//	)
//
// # Keys and Tags
//
// [Fields] is the one definition of how a struct becomes keys: the koanf tag
// names a segment (",squash" merges into the parent, "-" skips), pointers are
// followed, and a struct that decodes itself is one key. Every output names a
// value in one grammar — db.host, items[0].port, named["primary"].port, and
// items[*].port for any element — and [FieldFor] maps a key back to its
// declared field. Defaults, environment names, redacted printing,
// [PrintUsage] and the kongprovider flag bridge all use it. The other tags:
//
//   - default:"..." — the lowest-precedence value; key=value entries for a map
//   - sep:";" — the separator that splits a list's strings, or "none"
//   - format:"json" — a string set for a section, map or list is its JSON
//   - alias:"old" — former keys, still read, with a warning
//   - env:"PORT" — more variables, read as written, below the prefixed one
//   - example:"..." — a sample value for starter files, usage and the schema
//   - validate:"..." — go-playground rules
//   - desc:"..." — the description every output shows
//   - sensitive:"true" — redacted everywhere; "false" exempts a field its
//     name would redact
//   - secret:"<ref>" — resolved by [SecretsFrom], and redacted
//
// Invalid sensitive, alias, env, sep, format and example tags, a default on
// a section, and malformed defaults fail with [ErrSchema] before sources are
// read. [KeyOf] names a field from its address.
//
// # Sources
//
// Highest wins: override providers ([WithOverrideProvider], e.g. CLI flags),
// environment variables ([WithEnvPrefix]), files ([WithFile],
// [WithRequiredFile]; YAML or JSON by suffix, any parser through
// [WithFileParser]), base providers ([WithBaseProvider]), programmatic
// defaults ([WithDefaults]), and default tags. Any koanf.Provider loads at
// either provider layer, with any parser; a [ContextProvider] is read with
// the load's context.
//
// With WithEnvPrefix("APP"), APP_DB_MAX_CONNS sets db.max_conns: the name is
// looked up in the key tree, so an underscore inside a key survives. A map of
// structs reads APP_FEATURES_<KEY>_<FIELD>, any other map APP_LABELS_<KEY>. A
// field's env tag adds variables read as written, without the prefix
// (env:"PORT"); the prefixed variable wins over them. A variable that fits two
// keys fails the load with [ErrValidation] naming both; one that fits none is
// not loaded. [WithEnviron] replaces os.Environ.
//
// Keys match fields regardless of letter case, as decoding does; each
// source's keys are rewritten into the fields' spelling before the sources
// merge, so precedence holds whatever case a source used. A field's alias tag
// names former keys rewritten the same way, so a renamed setting keeps
// reading deployed files; the load logs a warning naming each. An element
// struct's default tags apply to every map entry and slice element; an entry
// set to null stays null — write {} for an entry that takes its defaults.
//
// # Pipeline
//
//	merge → decode, unknown keys refused → secrets (WithSecretPopulator) →
//	post-load (WithPostLoad) → validate (the validator, then a Validatable's
//	Validate method)
//
// Decoding adds string → time.Duration, comma-separated string → slice (a
// field's sep tag names another separator, or none), key=value entries → map
// and encoding.TextUnmarshaler to mapstructure's weak typing; [WithDecodeHook]
// hooks precede the built-in hooks, and a sep field is split before any hook.
// A key no field decodes fails the load — a misspelled key cannot leave a
// service on the default it meant to override — unless a [WithUnknownKeys]
// handler accepts it, say after logging a warning; [WithAllowUnknownKeys]
// accepts every one.
// Put derivation and normalization in a post-load hook, not after Load
// returns: the hook runs inside the load, so a Reloader applies it before
// deciding whether to swap.
//
// # Errors
//
// Every error wraps one category sentinel — [ErrValidation], [ErrFileRead],
// [ErrFileParse], [ErrSource], [ErrSecretProvider], [ErrPostLoad], and for a
// reload [ErrReload], [ErrReloadInProgress], [ErrReloaderStopped] — together
// with its cause, so errors.Is and errors.As reach either. The package defines
// no error codes; map the categories to your own. The errors it writes quote
// no value meant for a sensitive field: a decode failure on a field that is,
// or could hold, a sensitive value — a decode hook's failure included — names
// the field and type only, and a [ParseError] names the source and line only.
// A failure on an ordinary field keeps its decoder's message, which may quote
// the value. Errors your providers, secret populator, post-load hooks and
// validator return pass through as written.
//
// # Hot Reload
//
//	reloader, err := config.NewReloader[AppConfig](ctx,
//	    []config.Option{config.WithFile("config.yaml"), config.WithEnvPrefix("APP")},
//	    config.WithFileWatch(0), // 0 = DefaultFileWatchDebounce
//	    config.WithSignalReload(syscall.SIGHUP),
//	    config.WithApplier(config.Applier[AppConfig]{
//	        Name:     "server",
//	        Prefixes: []string{"timeout"},
//	        Apply: func(ctx context.Context, ev config.ApplyEvent[AppConfig]) (config.ApplyResult, error) {
//	            server.SetTimeout(ev.Desired.Timeout) // ev.Changes names what it owes
//	            return config.ApplyResult{}, nil
//	        },
//	    }),
//	)
//	if err != nil {
//	    return err
//	}
//	if err := reloader.Arm(ctx); err != nil {
//	    return err // a trigger cannot be delivered, e.g. an unwatchable directory
//	}
//	go func() {
//	    if err := reloader.Run(ctx); err != nil {
//	        logger.Error("config reloader ended", "error", err)
//	    }
//	}()
//	defer func() {
//	    if err := reloader.Stop(shutdownCtx); err != nil {
//	        logger.Warn("config reloader did not stop in time", "error", err)
//	    }
//	}()
//
//	cfg := reloader.Config() // one atomic load
//
// A reload runs the whole pipeline and swaps only on success, returning a
// [ReloadResult]: the [Snapshot] it published and the keys that changed. A
// failed one keeps the previous config and is reported to
// [WithOnReloadError], the log and [Reloader.Stats]. [Reloader.Snapshot]
// returns the config, its generation and the reload stats from one moment,
// and encodes to JSON for a status endpoint. [WithApplier] registers code
// that pushes a published config into the running process — a log level, a
// pool's limits — by key prefix: each is called with the changes it owns,
// measured from what it last acknowledged, offered again after a failure,
// and what no applier applies is listed as waiting for a restart. Reloads are
// serialized: an automatic trigger waits for
// one in flight and then reads the sources again, while a manual
// [Reloader.Reload] returns [ErrReloadInProgress]. For a source the reloader
// cannot watch, call [Reloader.SourceChanged]. [NewReloaderFunc] gives a
// caller's own loader the same lifecycle. Published configs are shared by
// every reader and must be treated as immutable.
//
// # File Watch Limits
//
// [WithFileWatch] detects direct writes, rename-based saves, symlink swaps of
// the file or of any directory on its path, and directories removed,
// recreated or replaced further up — including Kubernetes ConfigMap and
// Secret volume updates. Paths resolve as the kernel resolves them, and a
// periodic recheck finds what sends no event: an ancestor directory renamed
// away and recreated, or — on macOS and BSD — a relink whose only trace is a
// temporary name renamed over the link. It cannot detect sources that are
// not files (environment variables, providers): use [WithPollInterval] or
// SourceChanged. A subPath-mounted ConfigMap never updates inside the
// container at all; mount the volume without subPath.
//
// # Diagnostics
//
// [Diff] lists every key whose value differs between two configs, comparing
// decoded values, with sensitive values redacted.
//
// [PrintRedacted], [RedactedMap], [RedactedTree] and [PrintEnv] show a config
// with every sensitive value redacted: tagged sensitive:"true", beneath such a
// field, named like a credential (unless tagged sensitive:"false"), or under
// such a map key. [PrintUsage] lists every key an operator can set with its
// environment variable, type, default and description; a default that is, or
// could hold, a sensitive value is withheld ([Field.PrintableDefault]), as is
// such an example tag's ([Field.PrintableExample]). [PrintTemplate] writes the
// config file a struct describes: every key with its default, which loads back
// as the defaults, and a key with none commented out, showing its example.
// [LoadSnapshot], or a reloader [WithProvenance], records where each value
// came from — the default, file, environment variable, provider or flag the
// merge kept, and the stages that changed it after decoding — and
// [PrintExplain] prints it, values redacted. [JSONSchema] describes the config
// file for editors and linters — advisory, Load being the authority.
package config
