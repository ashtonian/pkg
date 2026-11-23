# config cookbook

Complete programs that use [`config`](../../README.md) the way a service does, each with a test. The blocks below are excerpts of the files they name, and a test fails when one drifts from its file.

| Recipe | Shows |
|---|---|
| [Kubernetes ConfigMap and Secret volumes](#kubernetes-configmap-and-secret-volumes) | The file watch on a ConfigMap volume; Secret keys through koanf's `k8smount` provider and its watch |
| [Settings in a database](#settings-in-a-database) | A settings table read on every reload through `NewMapProvider`, tested on SQLite |
| [Settings a service writes](#settings-a-service-writes) | An admin API that stores `CanonicalKey` rows, checks each write with the service's own load, serializes writers across replicas and reloads the table through `WithOverrideEntries`, tested on SQLite |
| [AWS Secrets Manager](#aws-secrets-manager) | `secret` tags resolved through `SecretsFrom`, per environment |
| [HashiCorp Vault](#hashicorp-vault) | A KV v2 secret as config keys, through koanf's Vault provider |
| [A whole service](#a-whole-service) | Kong flags, validation, hot reload, appliers, admin endpoints and graceful shutdown |

This is a module of its own, so the `config` module takes none of the SDKs and drivers it uses. Run it from this directory with `GOWORK=off go test ./...`, or `make cookbook-check` from the repository root; it builds against the working tree through `replace` directives.

## Kubernetes ConfigMap and Secret volumes

Mount the ConfigMap that holds `config.yaml` and the Secret that holds the credentials as volumes — without `subPath`, which the kubelet never updates. The file watch follows the ConfigMap's `..data` swap; koanf's `k8smount` provider reads each Secret key as a config key, and its own watch bridges to `SourceChanged`, so a rotated value or a key added later reloads. An invalid update keeps the last valid config. [`kubernetes/main.go`](kubernetes/main.go) has the manifest and the `main` that arms the reloader before serving.

<!-- recipe: kubernetes/main.go -->

```go
// secretVolume reads every key of a mounted Secret as a key of the config:
// the Secret key db.password sets db.password.
func secretVolume(dir string) *k8smount.K8SMount {
	return k8smount.Provider(dir, ".", k8smount.Opt{
		TransformFunc: func(key, value string) (string, any) { return key, strings.TrimSpace(value) },
	})
}

// newReloader layers the Secret over config.yaml from the ConfigMap volume
// and the environment, and watches config.yaml.
func newReloader(ctx context.Context, configDir string, secrets *k8smount.K8SMount) (*config.Reloader[AppConfig], error) {
	return config.NewReloader[AppConfig](ctx,
		[]config.Option{
			config.WithRequiredFile(filepath.Join(configDir, "config.yaml")),
			config.WithEnvPrefix("APP"),
			config.WithOverrideProvider(secrets, nil),
		},
		config.WithFileWatch(0),
	)
}

// watchSecrets reloads on any change to the Secret volume — a rotated value,
// or a key added later — through the provider's own watch. Any koanf
// provider with a Watch method (Consul, etcd, NATS, AppConfig) bridges the
// same way; SourceChanged coalesces a burst into one reload.
func watchSecrets(secrets *k8smount.K8SMount, reloader *config.Reloader[AppConfig], logger *slog.Logger) error {
	return secrets.Watch(func(_ any, err error) {
		if err != nil {
			logger.Error("secret volume watch stopped; rotations need a restart", "error", err)
			return
		}
		reloader.SourceChanged()
	})
}
```

## Settings in a database

`NewMapProvider` turns a query into a source read on every load. Strict keys and validation apply to the table as to a file: a row naming no field, or a value out of range, fails that reload and the previous settings stay live. A poll picks up edits; code that writes the table calls `SourceChanged` to apply its change at once, and so can a `LISTEN` loop. The connection string comes from a small `BootConfig` loaded first.

<!-- recipe: database/main.go -->

```go
// settings reads the settings table on every load: one row per key in the
// package's dotted grammar, each value text that decodes as an environment
// variable's would.
func settings(db *sql.DB, timeout time.Duration) (config.ContextProvider, error) {
	return config.NewMapProvider(func(ctx context.Context) (map[string]any, error) {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		rows, err := db.QueryContext(ctx, "SELECT key, value FROM settings")
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]any{}
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				return nil, err
			}
			out[key] = value
		}
		return out, rows.Err()
	}, ".")
}

// newReloader layers the table over config.yaml and the environment and
// reads it again every poll interval. Code that writes the table calls
// SourceChanged to apply its change without waiting for the poll.
func newReloader(ctx context.Context, db *sql.DB, boot *BootConfig) (*config.Reloader[AppConfig], error) {
	table, err := settings(db, boot.QueryTimeout)
	if err != nil {
		return nil, err
	}
	return config.NewReloader[AppConfig](ctx,
		[]config.Option{
			config.WithFile("config.yaml"),
			config.WithEnvPrefix("APP"),
			config.WithOverrideProvider(table, nil),
		},
		config.WithPollInterval(boot.PollInterval),
	)
}
```

## Settings a service writes

When the service writes the table itself — an admin API, a settings page — store each key as `config.CanonicalKey` spells it and load the table through `config.WithOverrideEntries`, so the writer and the loader share one key grammar: `limits.max`, `upstreams["api.internal"].timeout` (a map key holding a dot), `routes[1].path`. Every spelling of a key — a former name in an `alias` tag, another letter case — writes the one row, and `Key.Within` finds the rows beneath a key, which its write replaces and its `null` deletes. A key the schema no longer reads, its field removed by a release, may still be written as `null`: that deletes the rows stored at and beneath it that the schema does not read either. Any other value for such a key, or a `null` with no such row stored, is refused as unknown, and a spelling the schema refuses never deletes a row it reads.

A write is checked by the load that will read it. The service's options come from one builder; a write builds the table it would leave and loads it with those options, the table read replaced by that one, so `limits.min` and `limits.max` move together, the file and the environment beneath the table are read as the next reload reads them, and a table the service would refuse is never committed. `DecodeEdits` over the live config is not that check: it misses a lower source that changed and a row deleted since the config loaded. When the settings fail to load without the write as well, the fault is beneath it — in the file, the environment or a row already stored — and the write is a 503, logged at `Error`. A refused write is a 422 whose body names the client's own keys and never a value.

Writes take turns across replicas: each updates one lock row as its first statement, which the database holds until the write ends, so each reads the table the one before it left. The transaction runs at `READ COMMITTED` whatever the server's default, the level at which a write that waited reads what the one before it committed; SQLite's driver ignores the level, since its transactions are serializable and the lock row's update takes its write lock. A configured cap bounds the table, and values are read with `UseNumber`, so integers keep every digit. The pool, the request timeouts and caps, and the log level (`warn` by default, with one startup entry) are `BootConfig` fields. [`settingsstore/main.go`](settingsstore/main.go) has the `PUT /admin/settings` handler and the `main` that serves it; its test runs every path on SQLite, two replicas writing at once among them.

| Command | Does |
|---|---|
| `settingsstore migrate` | Applies `Schema`, which creates only what is missing, and exits. Run it once per release, before the replicas start, with a role that may create tables. |
| `settingsstore` | Serves the admin API. It runs no DDL, so its role needs only `SELECT`, `INSERT`, `UPDATE` and `DELETE` on `settings` and `settings_lock`, and replicas never race one another creating tables, which Postgres can refuse with a unique violation. Started against a database without the schema, it exits naming the migrate step. |

Both commands read the variables below.

| Variable | Type | Default | Description |
|---|---|---|---|
| `SETTINGS_DATABASE_URL` | string | required | The Postgres connection string; sensitive, withheld from the startup entry |
| `SETTINGS_CONFIG_FILE` | path | `config.yaml` | The optional file beneath the environment and the table |
| `SETTINGS_ADDR` | address | `:8080` | The admin API's listen address |
| `SETTINGS_LOG_LEVEL` | `debug`, `info`, `warn`, `error` | `warn` | The JSON log's level; `info` adds one entry per write |
| `SETTINGS_MAX_OPEN_CONNS` | int | `4` | The pool's open connections |
| `SETTINGS_MAX_IDLE_CONNS` | int | `2` | The pool's idle connections, at most the open ones |
| `SETTINGS_CONN_MAX_IDLE_TIME` | duration | `5m` | How long a connection idles before it closes |
| `SETTINGS_CONN_MAX_LIFETIME` | duration | `30m` | How long a connection lives |
| `SETTINGS_QUERY_TIMEOUT` | duration | `5s` | The bound on a read of the table, and on a write with its wait for its turn |
| `SETTINGS_POLL_INTERVAL` | duration | `30s` | How often the reloader reads the table besides after a write |
| `SETTINGS_MAX_ROWS` | int | `1000` | The table's row cap: a write past it is refused, and a read of a table past it fails |
| `SETTINGS_MAX_BODY_BYTES` | int | `1048576` | The largest request body; a larger one is a 413 |
| `SETTINGS_READ_HEADER_TIMEOUT`, `SETTINGS_READ_TIMEOUT` | duration | `5s`, `10s` | The server's bounds on reading a request's headers, and the whole request |
| `SETTINGS_WRITE_TIMEOUT` | duration | `15s` | The server's bound on a response; longer than the query timeout |
| `SETTINGS_IDLE_TIMEOUT` | duration | `60s` | How long a keep-alive connection idles |
| `SETTINGS_SHUTDOWN_TIMEOUT` | duration | `10s` | The drain deadline on `SIGTERM` |

A write that times out waiting for its turn — behind another write of the same replica, for a pooled connection, or for the lock row another replica holds — is contention: a 503 with `reason` `busy` and a `Retry-After` of the query timeout, logged at `Warn`. Frequent ones mean writes take too long or arrive too fast for `SETTINGS_QUERY_TIMEOUT`. Every other 503 is logged at `Error`: `the settings fail to load without this write` means the service's own settings do not load even with the write left out, so no client is at fault. The entry names what to check — `SETTINGS_CONFIG_FILE`, the `APP_*` variables and the settings table — and the wrapped error names the key that fails. Fix the file or the variable and redeploy, or fix or delete the row — a row whose field a release removed is deleted by writing its key as `null` — and the next poll recovers; `taking the settings lock` means the lock row is gone — run `settingsstore migrate`, which restores it; anything else is the database. While the settings do not load, every reload fails and the service keeps its previous config.

<!-- recipe: settingsstore/main.go -->

```go
// Sources are how the service loads AppConfig: Options builds the options,
// the table read through entries, and Beneath names the sources beneath the
// table, for an operator to check when the settings fail to load.
type Sources struct {
	Options func(entries Entries) []config.Option
	Beneath string
}

// appSources layers the table over the file and APP_* variables.
func appSources(file string, logger *slog.Logger) Sources {
	return Sources{
		Options: func(entries Entries) []config.Option {
			return []config.Option{
				config.WithFile(file),
				config.WithEnvPrefix("APP"),
				config.WithOverrideEntries(entries),
				config.WithLogger(logger),
			}
		},
		Beneath: file + " and the APP_* variables",
	}
}
```

<!-- recipe: settingsstore/main.go -->

```go
// Write applies edits to the table, all or nothing, and returns the keys it
// wrote: each key, spelled canonically, set to its value and replacing the
// rows stored beneath it, or, set to nil, deleted with them. A key the
// schema does not read may be set only to nil, to delete the rows a release
// that removed its field left. Unless the table the write leaves loads with
// the service's options and fits the row cap, the write is refused with a
// *RefusedError and nothing changes.
func (s *Settings) Write(ctx context.Context, edits map[string]any) ([]string, error) {
	writes, unread, err := canonical(edits)
	if err != nil || len(writes) == 0 && len(unread) == 0 {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	select {
	case s.writing <- struct{}{}:
		defer func() { <-s.writing }()
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for this replica's other settings write: %w: %w", ErrBusy, context.Cause(ctx))
	}
	// At READ COMMITTED each statement reads what committed before it, so a
	// write that waited for the lock row reads the table the write before it
	// left; at REPEATABLE READ or SERIALIZABLE, a server default some
	// deployments set, Postgres refuses the waiting write's update instead.
	// SQLite's driver ignores the level: its transactions are serializable,
	// and updating the lock row takes the database's write lock.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("beginning a settings write: %w", busy(ctx, err))
	}
	defer func() { _ = tx.Rollback() }() // after Commit, a no-op
	// Updating the lock row is the write's first statement: the database
	// holds the row until the write ends, so writes from every replica take
	// turns, and each reads the table the one before it left.
	locked, err := tx.ExecContext(ctx, "UPDATE settings_lock SET writes = writes + 1 WHERE id = 1")
	if err != nil {
		return nil, fmt.Errorf("taking the settings lock: %w", busy(ctx, err))
	}
	n, err := locked.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("taking the settings lock: %w", err)
	}
	if n != 1 {
		return nil, errors.New("taking the settings lock: settings_lock has no row 1; run `settingsstore migrate`")
	}
	stored, err := readRows(ctx, tx, s.maxRows)
	if err != nil {
		return nil, err
	}
	if missing := unstored(unread, stored); len(missing) > 0 {
		return nil, &RefusedError{Reason: ReasonUnknown, Keys: missing}
	}
	keys := append(slices.Collect(maps.Keys(writes)), unread...)
	slices.Sort(keys)
	table, deleted := apply(stored, writes, unread)
	if len(table) > s.maxRows {
		return nil, &RefusedError{Reason: ReasonFull, Keys: keys}
	}
	if err := s.check(ctx, stored, table, keys); err != nil {
		return nil, err
	}
	for _, key := range deleted {
		if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key = $1", key); err != nil {
			return nil, fmt.Errorf("deleting %s: %w", key, err)
		}
	}
	for _, key := range keys {
		if writes[key] == nil {
			continue
		}
		text, err := json.Marshal(writes[key])
		if err != nil {
			return nil, fmt.Errorf("encoding the value of %s: %w", key, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = excluded.value",
			key, string(text)); err != nil {
			return nil, fmt.Errorf("storing %s: %w", key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing a settings write: %w", err)
	}
	return keys, nil
}
```

<!-- recipe: settingsstore/main.go -->

```go
// check loads table, the one a write of keys leaves, with the service's
// options. A table that does not load refuses the write, unless the
// settings do not load without the write either: then the fault lies in a
// source beneath it or in a row already stored, and no client can fix it by
// retrying.
func (s *Settings) check(ctx context.Context, stored, table map[string]any, keys []string) error {
	_, err := config.Load[AppConfig](ctx, s.sources.Options(fixed(table))...)
	if err == nil || !errors.Is(err, config.ErrValidation) && !errors.Is(err, config.ErrKey) {
		return err
	}
	if _, storedErr := config.Load[AppConfig](ctx, s.sources.Options(fixed(stored))...); storedErr != nil {
		return fmt.Errorf("the settings fail to load without this write; check %s and the settings table: %w", s.sources.Beneath, storedErr)
	}
	reason := ReasonInvalid
	if unknown, ok := errors.AsType[*config.UnknownKeysError](err); ok && len(unknown.Keys) > 0 {
		reason = ReasonUnknown // a key inside a value the write sets
	}
	return &RefusedError{Reason: reason, Keys: keys, Err: err}
}
```

## AWS Secrets Manager

Tag the fields with their secrets and resolve them with `SecretsFrom`. The refs are relative to a per-environment prefix, so one config type serves every environment; `#member` reads one key of a JSON secret, each secret is fetched once per load, and `,optional` leaves a field to the file when the store has no such secret. A poll picks up rotations; a fetch that fails keeps the previous config.

<!-- recipe: awssecrets/main.go -->

```go
type DBConfig struct {
	Host     string `koanf:"host" default:"localhost"`
	User     string `koanf:"user" secret:"db#username"`
	Password string `koanf:"password" secret:"db#password" validate:"required"`
}

type AppConfig struct {
	DB         DBConfig `koanf:"db"`
	WebhookKey string   `koanf:"webhook_key" secret:"webhook-key,optional"` // a dev file may set it instead
}
```

<!-- recipe: awssecrets/main.go -->

```go
// fetchSecret reads the secret prefix+ref. One that does not exist is
// config.ErrSecretNotFound, so an ,optional tag leaves its field as the
// sources set it.
func fetchSecret(sm secretValues, prefix string) func(ctx context.Context, ref string) (string, error) {
	return func(ctx context.Context, ref string) (string, error) {
		out, err := sm.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(prefix + ref)})
		var missing *types.ResourceNotFoundException
		switch {
		case errors.As(err, &missing):
			return "", fmt.Errorf("%w: %s%s", config.ErrSecretNotFound, prefix, ref)
		case err != nil:
			return "", err
		case out.SecretString == nil:
			return "", fmt.Errorf("secret %s%s is binary; store it as a string", prefix, ref)
		}
		return *out.SecretString, nil
	}
}

func newReloader(ctx context.Context, sm secretValues, boot *BootConfig) (*config.Reloader[AppConfig], error) {
	return config.NewReloader[AppConfig](ctx,
		[]config.Option{
			config.WithFile("config.yaml"),
			config.WithEnvPrefix("APP"),
			config.WithSecretPopulator(config.SecretsFrom(fetchSecret(sm, boot.SecretPrefix))),
		},
		config.WithPollInterval(boot.RotationCheck),
	)
}
```

[`awssecrets/main.go`](awssecrets/main.go) builds the client with an HTTP timeout from `BootConfig`.

## HashiCorp Vault

koanf's Vault provider reads a KV v2 secret as config keys — `db.password` sets `db.password` — layered over the file and the environment. A key naming no field fails the load, so a typo in Vault does not pass silently; a poll picks up rotations. The token is one a Vault Agent sidecar keeps renewed.

<!-- recipe: vault/main.go -->

```go
// vaultSource reads the KV v2 secret at boot.Vault.Path on every load: its
// data, not its metadata, each key split on "." into the config's tree.
func vaultSource(boot *BootConfig) (*vault.Vault, error) {
	return vault.Provider(vault.Config{
		Address:     boot.Vault.Address,
		Token:       boot.Vault.Token,
		Path:        boot.Vault.Path,
		Delim:       ".",
		Timeout:     boot.Vault.Timeout,
		ExcludeMeta: true,
	})
}

// newReloader layers the secret over config.yaml and the environment, and
// reads it again every poll.
func newReloader(ctx context.Context, secrets koanf.Provider, poll time.Duration) (*config.Reloader[AppConfig], error) {
	return config.NewReloader[AppConfig](ctx,
		[]config.Option{
			config.WithFile("config.yaml"),
			config.WithEnvPrefix("APP"),
			config.WithOverrideProvider(secrets, nil),
		},
		config.WithPollInterval(poll),
	)
}
```

## A whole service

One struct is the schema for the file, `APP_*` variables, the flags and their help, and validation — tags plus a `Validate` method for the cross-field rule. It is embedded in the two commands that use it, and kongprovider loads it while Kong parses, before Kong validates: `Validate` and the command's fields see the file, the environment and the flags, never Kong's defaults alone, and `template` and `schema`, which hold no config, run without a valid one. `serve` gets a reloader that watches the file and `SIGHUP`; flags keep winning on every reload. Appliers push the live parts — the log level, and the keys handlers read from `Config` on each request — and the listener's settings, which the command reads from its own fields, the startup config, are reported as waiting for a restart. [`service/main.go`](service/main.go) adds the routes, an admin status and reload endpoint behind a constant-time token check, and graceful shutdown.

<!-- recipe: service/main.go -->

```go
type HTTPConfig struct {
	Addr              string        `koanf:"addr" default:":8080" desc:"Listen address"`
	ReadHeaderTimeout time.Duration `koanf:"read_header_timeout" default:"5s" validate:"min=1ms" desc:"Time to read request headers"`
	ShutdownTimeout   time.Duration `koanf:"shutdown_timeout" default:"15s" validate:"min=1ms" desc:"Drain deadline on SIGTERM"`
}

type LogConfig struct {
	Level string `koanf:"level" default:"info" validate:"oneof=debug info warn error" desc:"Log level"`
}

// AppConfig is the whole schema: keys, defaults, validation, help and flags.
type AppConfig struct {
	HTTP       HTTPConfig `koanf:"http" embed:"" prefix:"http-"`
	Log        LogConfig  `koanf:"log" embed:"" prefix:"log-"`
	Greeting   string     `koanf:"greeting" default:"hello" desc:"What GET / answers"`
	AdminToken string     `koanf:"admin_token" validate:"required" kong:"-"` // file or APP_ADMIN_TOKEN only
}

// Validate holds the rule no tag states. It runs on the loaded config, on
// every load and reload.
func (c *AppConfig) Validate() error {
	if strings.TrimSpace(c.Greeting) == "" {
		return errors.New("greeting must not be blank")
	}
	return nil
}

// ServeCmd and PrintCmd hold the config; TemplateCmd and SchemaCmd need none,
// so they run without a valid one.
type CLI struct {
	Config string `type:"path" default:"service.yaml" help:"Config file."`

	Serve    ServeCmd    `cmd:"" default:"withargs" help:"Run the service."`
	Print    PrintCmd    `cmd:"" help:"Print the effective configuration, secrets redacted."`
	Template TemplateCmd `cmd:"" help:"Write a starter config file."`
	Schema   SchemaCmd   `cmd:"" help:"Write the config file's JSON Schema."`
}
```

<!-- recipe: service/main.go -->

```go
// loading returns the Kong options that load the config, while Kong parses,
// for the command that runs: for serve a reloader over the file, APP_* and
// the flags, with an applier for each part that changes live — the log
// level, set into level, and the greeting and admin token, which handlers
// read from Config on every request; for print the same sources, once. The
// http section is the listener's, fixed at start: a change to it waits for a
// restart.
func loading(ctx context.Context, cli *CLI, logger *slog.Logger, level *slog.LevelVar) ([]kong.Option, error) {
	var zero AppConfig
	levelKey, err := config.KeyOf(&zero, &zero.Log.Level)
	if err != nil {
		return nil, err
	}
	greetingKey, err := config.KeyOf(&zero, &zero.Greeting)
	if err != nil {
		return nil, err
	}
	tokenKey, err := config.KeyOf(&zero, &zero.AdminToken)
	if err != nil {
		return nil, err
	}
	sources := func(*kong.Context) ([]config.Option, error) {
		return []config.Option{config.WithFile(cli.Config), config.WithLogger(logger)}, nil
	}
	return []kong.Option{
		kongprovider.LoadReloader(ctx, &cli.Serve.AppConfig, []string{"APP"}, sources,
			config.WithFileWatch(0),
			config.WithSignalReload(syscall.SIGHUP),
			config.WithApplier(config.Applier[AppConfig]{
				Name:         "log",
				Prefixes:     []string{levelKey},
				ApplyInitial: true,
				Apply: func(_ context.Context, ev config.ApplyEvent[AppConfig]) (config.ApplyResult, error) {
					return config.ApplyResult{}, level.UnmarshalText([]byte(ev.Desired.Log.Level))
				},
			}),
			config.WithApplier(config.Applier[AppConfig]{
				Name:     "handlers",
				Prefixes: []string{greetingKey, tokenKey},
				Apply: func(context.Context, config.ApplyEvent[AppConfig]) (config.ApplyResult, error) {
					return config.ApplyResult{}, nil // read from Config on every request
				},
			}),
		),
		kongprovider.Load(ctx, &cli.Print.AppConfig, []string{"APP"}, sources),
		kong.BindTo(ctx, (*context.Context)(nil)),
		kong.Bind(logger),
	}, nil
}
```

<!-- recipe: service/main.go -->

```go
// ServeCmd's fields are the startup config: the listener's settings for the
// life of the process.
type ServeCmd struct {
	AppConfig `embed:""`
}

// Run serves until ctx ends, reloading the config as its sources change.
func (c *ServeCmd) Run(ctx context.Context, reloader *config.Reloader[AppConfig], logger *slog.Logger) error {
	if err := reloader.Arm(ctx); err != nil {
		return err
	}
	go func() {
		if err := reloader.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.ErrorContext(ctx, "config reloader ended", "error", err)
		}
	}()

	server := &http.Server{Addr: c.HTTP.Addr, Handler: routes(reloader), ReadHeaderTimeout: c.HTTP.ReadHeaderTimeout}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()
	logger.InfoContext(ctx, "serving", "addr", c.HTTP.Addr)

	var err error
	select {
	case err = <-serveErr:
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.HTTP.ShutdownTimeout)
	defer cancel()
	return errors.Join(err, server.Shutdown(shutdownCtx), reloader.Stop(shutdownCtx))
}
```
