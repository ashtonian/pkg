# config cookbook

Complete programs that use [`config`](../../README.md) the way a service does, each with a test. The blocks below are excerpts of the files they name, and a test fails when one drifts from its file.

| Recipe | Shows |
|---|---|
| [Kubernetes ConfigMap and Secret volumes](#kubernetes-configmap-and-secret-volumes) | The file watch on a ConfigMap volume; Secret keys through koanf's `k8smount` provider and its watch |
| [Settings in a database](#settings-in-a-database) | A settings table read on every reload through `NewMapProvider`, tested on SQLite |
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

One struct is the schema for the file, `APP_*` variables, the flags and their help, and validation — tags plus a `Validate` method for the cross-field rule. The reloader watches the file and `SIGHUP`; flags keep winning on every reload. Appliers push the live parts — the log level, and the keys handlers read from `Config` on each request — and the listener's settings are reported as waiting for a restart. [`service/main.go`](service/main.go) adds the routes, an admin status and reload endpoint behind a constant-time token check, and graceful shutdown.

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

// Validate holds the rule no tag states.
func (c *AppConfig) Validate() error {
	if strings.TrimSpace(c.Greeting) == "" {
		return errors.New("greeting must not be blank")
	}
	return nil
}

type CLI struct {
	Config    string `type:"path" default:"service.yaml" help:"Config file."`
	AppConfig `embed:""`

	Serve    struct{} `cmd:"" default:"1" help:"Run the service."`
	Print    struct{} `cmd:"" help:"Print the effective configuration, secrets redacted."`
	Template struct{} `cmd:"" help:"Write a starter config file."`
	Schema   struct{} `cmd:"" help:"Write the config file's JSON Schema."`
}
```

<!-- recipe: service/main.go -->

```go
// newReloader loads the config from the file, APP_* and the flags kctx
// parsed, and registers an applier for each part that changes live: the log
// level, set into level, and the greeting and admin token, which handlers
// read from Config on every request. The http section is the listener's,
// fixed at start: a change to it waits for a restart.
func newReloader(ctx context.Context, cli *CLI, kctx *kong.Context, logger *slog.Logger, level *slog.LevelVar) (*config.Reloader[AppConfig], error) {
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
	return config.NewReloader[AppConfig](ctx,
		[]config.Option{
			config.WithFile(cli.Config),
			config.WithEnvPrefix("APP"),
			kongprovider.WithOverridesFromStruct(kctx, &cli.AppConfig),
			config.WithLogger(logger),
		},
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
	)
}
```
