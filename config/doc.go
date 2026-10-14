// Package config provides typed configuration loading on top of koanf:
// struct-tag defaults, YAML files, environment variables and CLI flags merged
// in a fixed precedence, then secrets, post-load hooks and validation in one
// pipeline. [Reloader] re-runs that pipeline on change and swaps only a config
// that passed it; [PrintRedacted] and [ToMap] print a config with its secrets
// masked.
//
// Secret backends are the caller's, wired in via [WithSecretPopulator]; the
// package depends on none.
//
// # Quick Start
//
//	type AppConfig struct {
//	    Host    string        `koanf:"host" default:"localhost" desc:"Server hostname"`
//	    Port    int           `koanf:"port" default:"8080" validate:"min=1,max=65535"`
//	    Timeout time.Duration `koanf:"timeout" default:"30s"`
//	}
//
//	cfg, err := config.GetConfig[AppConfig](ctx,
//	    config.WithEnvPrefix("APP"),
//	    config.WithConfigFile("./config.yaml"),
//	)
//
// # Struct Tags and Keys
//
//   - koanf:"name"         - key segment; ",squash" merges into the parent
//   - koanf:"-"            - not a config key
//   - default:"value"      - lowest-precedence default
//   - validate:"required"  - go-playground/validator rules
//   - sensitive:"true"     - redacted by PrintRedacted/ToMap; on a struct,
//     slice or map, everything inside it is redacted
//   - desc:"description"   - shown by WithIncludeDescriptions
//   - secret:"key"         - resolved by the WithSecretPopulator closure
//   - flag:"flagname"      - Kong flag name (kongprovider sub-package)
//
// [Fields] is the one definition of how these tags become koanf paths;
// defaults, environment-variable names, printing and the Kong bridge all use
// it. With WithEnvPrefix("APP"), APP_DB_MAX_CONNS sets db.max_conns, for a
// map of structs APP_FEATURES_AUDIT_LOGS_MAX_PAGE_SIZE sets
// features.audit_logs.max_page_size, and for any other map APP_LABELS_TEAM_NAME
// sets labels.team_name. A variable that fits two keys fails the load with
// ErrValidation naming both; one that fits none is not loaded.
//
// # Source Precedence
//
// Highest wins:
//
//  1. Extra providers (CLI flags via kongprovider, custom providers)
//  2. Environment variables
//  3. Config files (merged left-to-right)
//  4. Programmatic defaults (WithDefaults)
//  5. Struct tag defaults (default:"value")
//
// An optional file ([WithConfigFile]) may be absent; one that exists but
// cannot be read or parsed fails the load with [ErrFileRead] or
// [ErrFileParse], exactly as a required file does.
//
// # Load Pipeline
//
// After the sources are merged, GetConfig runs a fixed sequence:
//
//	unmarshal → secrets (WithSecretPopulator) → post-load (WithPostLoad) → validate
//
// Put derivation and normalization in a WithPostLoad hook, not after GetConfig
// returns. The hook runs inside the load, so a Reloader applies it before
// deciding whether to swap — normalization is then covered by
// validate-before-swap. The same work done afterwards is not: the new config
// is already live by the time it fails.
//
// Every error keeps both its category sentinel and the underlying cause, so
// errors.Is(err, ErrFileParse) and errors.As into the parser's or the
// filesystem's error both work, through GetConfig and through a reload.
//
// # CLI Flag Overrides
//
//	import "github.com/ashtonian/pkg/config/kongprovider"
//
//	cfg, err := config.GetConfig[AppConfig](ctx,
//	    config.WithEnvPrefix("APP"),
//	    kongprovider.WithOverridesFromTags[AppConfig](kongCtx),
//	)
//
// # Secret Injection
//
// The package does not import any secret backend. The consumer wires
// in a [SecretPopulator] closure that captures the chosen backend (e.g.
// Vault, AWS Secrets Manager):
//
//	cfg, err := config.GetConfig[MyConfig](ctx,
//	    config.WithSecretPopulator(func(ctx context.Context, target any) error {
//	        return secrets.Populate(ctx, provider, target)
//	    }),
//	)
//
// # Hot Reload
//
// Reloader[T] watches config sources and atomically swaps the config struct
// when changes are detected. Readers access the current config via Config()
// with zero contention (atomic pointer load). If a reload fails, the previous
// config is preserved.
//
//	reloader, err := config.NewReloader[AppConfig](ctx,
//	    []config.ReloadOption[AppConfig]{
//	        config.WithFileWatch[AppConfig](0), // 0 = DefaultFileWatchDebounce
//	        config.WithOnChange(func(old, new *AppConfig) {
//	            server.SetTimeout(new.Timeout)
//	        }),
//	    },
//	    config.WithLogger(logger),
//	    config.WithEnvPrefix("APP"),
//	    config.WithConfigFile("./config.yaml"),
//	)
//
//	if err := reloader.Arm(ctx); err != nil {
//	    return err // a trigger cannot be delivered, e.g. an unwatchable directory
//	}
//	go reloader.Start(ctx)
//	defer reloader.Stop(shutdownCtx)
//
//	cfg := reloader.Config() // hot path
//
//	mux.Handle("POST /admin/config/reload", reloader.ReloadHandler())
//
// Reloads are serialized. A watcher-driven reload (file change, poll, signal)
// that arrives while another reload runs waits for it and then reads the
// sources again, so a change is never lost. A manual Reload does not wait: it
// returns ErrReloadInProgress. Stop cancels an automatic reload in flight and
// waits for a manual one; once it returns nil no reload runs and no callback
// fires — safe to tear down whatever the callbacks touch. A Stop whose ctx
// ends first returns that error without the guarantee.
//
// Arm sets up the file watch and the signal registration and returns their
// error, so a caller can refuse to report ready on a reloader that cannot
// deliver what it was configured for; Start then serves what Arm set up,
// missing nothing in between. Start arms the triggers itself when Arm was not
// called, and returns the error.
//
// For a source the reloader cannot watch (a WithProvider store, a replicated
// log), call SourceChanged from whatever learns of the change: it never
// blocks, coalesces bursts, and the reload it triggers reads the change.
// Start serves those requests, so run it even with no watchers configured.
//
// # Reload Observability
//
// Reloads driven by Start's watchers do not surface an error to any caller, so
// register WithOnReloadError to catch them — a process whose reloads keep
// failing is serving stale config and should be able to say so. Stats()
// returns a snapshot (attempts, successes, failures, last trigger, last
// success/failure times, last error) suitable for export as metrics; the
// Trigger* constants are a closed vocabulary safe to use as a label.
//
// Alert on ErrReload. ErrReloadInProgress and ErrReloaderStopped are not
// failures — no pipeline ran and the live config is unchanged — and a reload
// whose context ended carries ctx.Err() instead of ErrReload.
//
// # File Watch Limits
//
// WithFileWatch detects direct writes, rename-based saves, symlink swaps of
// the file or of any directory on its path, and directories removed and
// recreated — including Kubernetes ConfigMap and Secret volume updates. It
// cannot detect sources with no filesystem event (env vars, WithProvider) —
// use WithPollInterval or SourceChanged for those — or, on macOS and BSD
// only, a relink whose sole trace is a temporary name renamed away, which
// polling catches. A subPath-mounted ConfigMap never updates inside the
// container at all, so neither watching nor polling sees a change; mount the
// volume without subPath. See WithFileWatch for detail.
package config
