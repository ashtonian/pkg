# Hot reload

[Documentation](README.md) · [Quick start](../README.md#quick-start) · [Go API](https://pkg.go.dev/github.com/ashtonian/pkg/config)

- [Applying changes](#applying-changes)
- [Declared reload classes](#declared-reload-classes)
- [File watching and Kubernetes](#file-watching-and-kubernetes)
- [A reload endpoint](#a-reload-endpoint)

```go
reloader, err := config.NewReloader[AppConfig](ctx,
	[]config.Option{
		config.WithFile("/etc/app/config.yaml"),
		config.WithEnvPrefix("APP"),
		config.WithLogger(logger),
	},
	config.WithFileWatch(0), // 0: the default 500ms debounce
	config.WithSignalReload(syscall.SIGHUP),
	config.WithOnReloadError(func(trigger string, err error) {
		reloadFailures.WithLabelValues(trigger).Inc()
	}),
)
if err != nil {
	return err // the initial load failed
}
if err := reloader.Arm(ctx); err != nil {
	return err // a trigger cannot be set up, e.g. an unwatchable directory
}
go func() {
	if err := reloader.Run(ctx); err != nil {
		logger.Error("config reloader ended", "error", err)
	}
}()
defer func() {
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := reloader.Stop(stopCtx); err != nil {
		logger.Warn("config reloader did not stop in time", "error", err)
	}
}()

cfg := reloader.Config() // one atomic load; call it on every use
```

| Call | Does |
|---|---|
| `NewReloader`, `NewReloaderFunc` | Runs the first load; its error fails construction. |
| `Arm(ctx)` | Sets up the file watch and signals and returns their error, so a service can refuse to report ready. Optional: `Run` arms itself. |
| `Run(ctx)` | Serves every trigger until `Stop` (returns nil) or `ctx` ends. |
| `Reload(ctx)` | A manual reload, returning what it published, the keys that changed and the edits a higher-precedence source kept from taking effect; `ErrReloadInProgress` if one is running. |
| `SourceChanged()` | Asks for a reload after a change the reloader cannot watch. Never blocks; bursts coalesce. |
| `Config()`, `Snapshot()`, `Stats()` | The live config; the config with its generation and reload history from one moment; the history alone. Lock-free. |
| `Stop(ctx)` | Stops the triggers, cancels an automatic reload and waits for a manual one. |

What a reloader guarantees:

- **Only a config that passed goes live.** A reload runs every source, secrets, hooks and validation before the swap. A failed reload keeps the previous config, and is reported to `WithOnReloadError`, the log and `Stats().LastError`, which carries its category.
- **No change is lost.** Reloads are serialized; an automatic trigger arriving during a reload waits and reads the sources again. Signal a change with `SourceChanged`, not by calling `Reload` from an event handler.
- **One moment, one snapshot.** The config, its generation (1 at boot, +1 per successful reload), accepted `UnknownKeys` report, `Shadowed` inputs and stats are published together. A clean publication clears the report; refused or canceled loads keep the prior one. An applier failure keeps the new publication and its report.
- **Configs are shared.** Every reader gets the same `*T`; treat it, maps and slices included, as immutable.

`WithOnReloadSuccess` callbacks run after each swap with the old and new configs and the changed keys. `NewReloaderFunc` gives a loader of your own the same lifecycle; name the files it reads with `WithWatchedFiles`. `ExampleNewReloader` shows a valid edit swapped in and an invalid one refused.

## Applying changes

`Config()` serves code that reads the config on every use. Code that captured a value at startup — a log level, a pool's limits, a router's routes — needs the change pushed to it. An applier owns keys by prefix and is called with the changes it owns after each published config:

```go
logLevel := new(slog.LevelVar)
reloader, err := config.NewReloader[AppConfig](ctx, loadOpts,
	config.WithFileWatch(0),
	config.WithApplier(config.Applier[AppConfig]{
		Name:     "logging",
		Prefixes: []string{"log.level"},
		Apply: func(ctx context.Context, ev config.ApplyEvent[AppConfig]) (config.ApplyResult, error) {
			logLevel.Set(parseLevel(ev.Desired.Log.Level))
			return config.ApplyResult{}, nil
		},
	}),
	config.WithApplier(config.Applier[AppConfig]{
		Name:     "database",
		Prefixes: []string{"db"},
		Apply: func(ctx context.Context, ev config.ApplyEvent[AppConfig]) (config.ApplyResult, error) {
			var restart []string
			for _, ch := range ev.Changes {
				if ch.Key == "db.host" {
					restart = append(restart, ch.Key) // a new host needs a new pool
				}
			}
			pool.SetMaxConns(ev.Desired.DB.MaxConns) // idempotent: a retry may repeat it
			return config.ApplyResult{RestartKeys: restart}, nil
		},
	}),
)
```

- **Ownership.** A prefix is a section (`db`), a value (`log.level`), a map entry (`features["audit"]`) or a pattern (`features[*].gate`); the most specific prefix owns a key.
- **Measured from what was applied.** An applier is called once per published config with the keys whose published value differs from what it last acknowledged. Returning nil acknowledges them all, except the `RestartKeys` it returns.
- **Failures are retried.** An error or a panic acknowledges nothing; the keys it was given are offered again, `Retry` set, until a call applies them. Appliers must be idempotent.
- **Structures need one owner.** A map entry or pointer section that appears or disappears applies live only through an applier that owns it and everything beneath it; otherwise it waits for a restart.
- **Everything else waits for a restart.** With appliers registered, `Snapshot().PendingRestart` lists every change no applier took — unowned keys, restart keys, blocked structures — until it is applied or reverted. Register a no-op applier for keys your code reads from `Config()` on every use.
- **Startup.** Set `ApplyInitial` to have an applier apply the first load before the constructor returns; otherwise build that state from `Config()` yourself. A subsystem that may not run registers its applier only when it runs.
- **Published is not applied.** A config an applier failed on stays live: `Reload` returns its result together with an `*ApplyError` (`ErrApply`). `Snapshot().Appliers` reports each applier's account, and `Snapshot().Applying` whether calls are still running.

Keeping the account is cheap: per applier, the reloader holds only the keys where what it applied differs from the published config, as copied values, and an applier that is up to date costs nothing.

## Declared reload classes

```go
type Database struct {
    Host     string `koanf:"host" reload:"restart"`
    MaxConns int    `koanf:"max_conns" reload:"live"`
}
```

`Field.Reload` resolves the nearest `reload` tag, including tags on enclosing
structs, maps, lists and squashed fields. Without one it is `restart`.
`live` and other nonempty single-word labels, such as `hot`, are live classes;
only `restart` requires a restart. A shared struct type that declares its own
reload tags at multiple schema paths fails with `ErrSchema`; place classes
on its owning fields instead.

Pass `WithReloadTags()` to enable this policy. A broad `db` applier then
receives `db.max_conns` changes while `db.host` remains in `PendingRestart`.
Restart changes are withheld during `ApplyInitial` too. Mixed structural
changes still require one live owner for the structure and all its children;
a restart child prevents the whole structural transition from applying live.
Reverting to the applied value clears the pending change.

`WithReloadTagChecks()` also enables the policy and checks that every live
schema key has an applier and every registered applier owns a live key.
Checks include collection patterns and run before reading sources: claiming
one literal map key cannot cover every possible entry. An untagged schema
retains prefix routing, including when either option is supplied. Metadata
is available without enabling routing, so callers can keep their own policy.

`RestartChanges(changes)` selects declared restart changes from a successful `Diff` for
an offline comparison. It does not account for missing/failed appliers or
blocked structural changes; runtime `PendingRestart` does.

## File watching and Kubernetes

`WithFileWatch` resolves each path the way the kernel does and watches every directory the resolution passes through, so it sees direct writes, rename-based saves, symlink swaps anywhere on the path — including Kubernetes ConfigMap and Secret volume updates, which swap a `..data` symlink rather than write the file — and directories removed or recreated. A periodic recheck catches what sends no event. It cannot see sources that are not files: poll them with `WithPollInterval` or call `SourceChanged`. A volume mounted with `subPath` never updates inside the container; mount it without. Symlinks directly under the filesystem root, such as `/var` and `/tmp` on macOS, are followed but not watched for re-pointing: they belong to the OS, and watching `/` would hold a descriptor per entry and can block on automount points like `/net`.

**A key meant to change live must be set only by sources that change.** Moving settings from environment variables into a mounted ConfigMap file, so they can change without a restart, works only once the variables are gone: the environment outranks files, and a running process's environment never changes. A variable left behind pins its key — the file's edit is read, merged beneath the variable and has no effect. Typed CLI flags pin their keys the same way for the life of the process, because kongprovider copies them when the reloader is built. The reload still succeeds and the generation advances, so the reloader reports it:

```txt
level=WARN msg="config input changed beneath a higher-precedence source; the change does not take effect" trigger=file_change generation=2 key=log_level source="file /etc/app/config.yaml" by="environment APP_LOG_LEVEL"
```

`ReloadResult.Shadowed` lists the same entries, `Snapshot().Shadowed` every input a higher source outranks at any time, and a `WithShadowedInputs` handler can refuse a load that has any. On a 2am page where an edit "did not apply": read `Snapshot().Shadowed` from the status endpoint, find the key, and remove the variable or flag its `by` names (or set the value there). See [shadowed inputs](sources.md#shadowed-inputs).

## A reload endpoint

The package mounts no HTTP routes; authentication is yours. Every result type encodes to JSON as is, with the config itself left out:

```go
mux.HandleFunc("POST /admin/config/reload", func(w http.ResponseWriter, r *http.Request) {
	result, err := reloader.Reload(r.Context())
	switch {
	case errors.Is(err, config.ErrReloadInProgress):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case errors.Is(err, config.ErrReloaderStopped):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	case err != nil && !errors.Is(err, config.ErrApply):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result) // generation, stats, changes, shadowed edits, applier outcomes
})
mux.HandleFunc("GET /admin/config/status", func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reloader.Snapshot())
})
```
