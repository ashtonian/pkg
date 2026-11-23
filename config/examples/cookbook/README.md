# config cookbook

Complete programs that use [`config`](../../README.md) the way a service does, each with a test. The blocks below are excerpts of the files they name, and a test fails when one drifts from its file.

| Recipe | Shows |
|---|---|
| [Kubernetes ConfigMap and Secret volumes](#kubernetes-configmap-and-secret-volumes) | The file watch on a ConfigMap volume; Secret keys through koanf's `k8smount` provider and its watch |
| [Settings in a database](#settings-in-a-database) | A settings table read on every reload through `NewMapProvider`, tested on SQLite |
| [Settings a service writes](#settings-a-service-writes) | An admin API that stores `CanonicalKey` rows, checks each write with the service's own load, serializes writers across replicas and reloads the table through `WithOverrideEntries`, tested on SQLite and PostgreSQL |
| [AWS Secrets Manager](#aws-secrets-manager) | `secret` tags resolved through `SecretsFrom`, per environment |
| [HashiCorp Vault](#hashicorp-vault) | A KV v2 secret as config keys, through koanf's Vault provider |
| [A whole service](#a-whole-service) | Kong flags, validation, hot reload, appliers, admin endpoints and graceful shutdown |

This is a module of its own, so the `config` module takes none of the SDKs and drivers it uses. It builds against the working tree through `replace` directives.

## Build, run and test

Run it from this directory with `GOWORK=off go test ./...`, or from the repository root:

| Command | Runs |
|---|---|
| `make cookbook-check` | `go mod tidy -diff`, `go vet` and every recipe's race tests. The PostgreSQL legs skip unless `COOKBOOK_POSTGRES_DSN` is set |
| `make lint-cookbook` | `go vet` on the declared Go floor, and staticcheck |
| `make cookbook-integration` | The database tests: the settings-store recipe's, each on SQLite and on the PostgreSQL server `COOKBOOK_POSTGRES_DSN` names, and its fixture's own |
| `eval "$(make -s cookbook-postgres-up)"` | Starts PostgreSQL 19 in Docker, the server CI's `cookbook-postgres` job provisions, and exports `COOKBOOK_POSTGRES_DSN`. `make cookbook-postgres-down` removes it |

| Variable | Default | Description |
|---|---|---|
| `COOKBOOK_POSTGRES_DSN` | unset | A PostgreSQL URL. Each test gets a schema of its own, first on the search path and dropped when the test ends, so tests share any server, the one `make parse-postgres-up` starts among them. Unset, the PostgreSQL legs skip |
| `COOKBOOK_REQUIRE_PG` | unset | `1` fails a PostgreSQL leg whose variable is unset instead of skipping it; CI's `cookbook-postgres` job sets it, so the job cannot pass without the server |
| `COOKBOOK_PG_IMAGE`, `COOKBOOK_PG_NAME`, `COOKBOOK_PG_PORT` | `postgres:19`, `cookbook-postgres`, `55433` | Make variables: the image, container name and host port `cookbook-postgres-up` uses |
| `COOKBOOK_PG_READY_SECONDS` | `60` | Make variable: how many seconds `cookbook-postgres-up` waits for the server to accept connections before it fails, naming the container |

A server that is set and unreachable fails the PostgreSQL legs rather than skipping them. The fixture is [`internal/pgtest`](internal/pgtest/pgtest.go), the twin of parse's `internal/dbtest`, which this module cannot import; a change to one's semantics belongs in both.

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

When the service writes the table itself — an admin API, a settings page — store each key as `config.CanonicalKey` spells it and load the table through `config.WithOverrideEntries`, so the writer and the loader share one key grammar: `limits.max`, `upstreams["api.internal"].timeout` (a map key holding a dot), `routes[1].path`. Every spelling of a key — a former name in an `alias` tag, another letter case — writes the one row, and `Key.Within` finds the rows beneath a key, which its write replaces and its `null` deletes. A row stored above a key — `limits` holding `{"max": 300}` when `limits.max` is written — has the key cut out of its value, under any spelling the value holds it by, in the same transaction and checked by the same load, so a set or a `null` beneath a row takes effect; a row left setting nothing is deleted. One write may not name both a key and a key within it, `limits` and `limits.max`: which wins would be the load's merge to decide, so the write is refused naming both, and the client sends the parent with the child folded in. A list or a scalar cannot be cut into, so a key beneath one a row stores, `routes[0].path` beneath a stored `routes` list, is refused: write the row's key whole. A release that removes a field leaves rows, or parts of rows' values, at keys the schema no longer reads. The table's source is under `config.WithUnknownEntries`, which claims the keys no field reads that only the table sets, by the decoder's own rules, so they stop no load: the service still starts, reloads and writes, and each load that finds any logs them at `Warn`. A key another source sets stays the load's, and fails it as before. A write's check claims them the same way, but refuses the write as unknown when one lies within a key the write sets. Such a key may still be written as `null`: that deletes the rows stored at and beneath it that the schema does not read either. Any other value for such a key, a `null` with no such row stored, or a key holding `[*]`, which names no one key, is refused as unknown, and a spelling the schema refuses never deletes a row it reads.

A write is checked by the load that will read it. The service's options come from one builder; a write builds the table it would leave and loads it with those options, the table read replaced by that one, so `limits.min` and `limits.max` move together, the file and the environment beneath the table are read as the next reload reads them, and a table the service would refuse is never committed. `DecodeEdits` over the live config is not that check: it misses a lower source that changed and a row deleted since the config loaded. When the settings fail to load without the write as well, the fault is beneath it — in the file, the environment or a row already stored — and the write is a 503, logged at `Error`. A refused write is a 422 whose body names the client's own keys, never a value, and its `reason`: `unknown` (a key names nothing the service reads), `repeated` (one key spelled two ways), `invalid` (the settings the write leaves do not load), `full` (the table would pass its cap), `enclosed` (a key lies inside a list or a scalar a stored row sets), `overlap` (one write names a key and a key within it), `too many` (the write names more keys than `SETTINGS_MAX_WRITE_KEYS`; `keys` is empty) or `too long` (a key, as it would be stored, is longer than `SETTINGS_MAX_KEY_BYTES`). The last two are refused before the write waits for its turn, so a write past them holds up no other.

Writes take turns across replicas: each updates one lock row as its first statement, which the database holds until the write ends, so each reads the table the one before it left. The transaction runs at `READ COMMITTED` whatever the server's default, the level at which a write that waited reads what the one before it committed; SQLite's driver ignores the level, since its transactions are serializable and the lock row's update takes its write lock. Configured caps bound the table in rows and in bytes, and each write in keys and in the bytes of each key; values are read with `UseNumber`, so integers keep every digit. While a write holds the lock row it reads each stored key once and finds the rows at, above and beneath each of its keys by the key's depth, so its work grows with the table and with the write, never with their product. The pool, the request timeouts and caps, and the log level (`warn` by default, with one startup entry) are `BootConfig` fields. [`settingsstore/main.go`](settingsstore/main.go) has the `PUT /admin/settings` handler and the `main` that serves it; its tests run every path on SQLite and on PostgreSQL ([Build, run and test](#build-run-and-test)): two replicas writing at once among them, and on PostgreSQL a write that waits on the lock row and then reads what the write holding it committed, on a session whose default isolation is `SERIALIZABLE`.

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
| `SETTINGS_LOG_LEVEL` | `debug`, `info`, `warn`, `error` | `warn` | The JSON log's level; `info` adds one entry per write, written or refused, with its `key_count`; `debug` adds the `keys` to it, a key whose field is sensitive shown as its field's pattern (`tokens[*]`), since a sensitive map's entries may be named by secrets |
| `SETTINGS_MAX_OPEN_CONNS` | int | `4` | The pool's open connections |
| `SETTINGS_MAX_IDLE_CONNS` | int | `2` | The pool's idle connections, at most the open ones |
| `SETTINGS_CONN_MAX_IDLE_TIME` | duration | `5m` | How long a connection idles before it closes |
| `SETTINGS_CONN_MAX_LIFETIME` | duration | `30m` | How long a connection lives |
| `SETTINGS_QUERY_TIMEOUT` | duration | `5s` | The bound on a read of the table, and on a write with its wait for its turn |
| `SETTINGS_POLL_INTERVAL` | duration | `30s` | How often the reloader reads the table besides after a write |
| `SETTINGS_MAX_ROWS` | int, at most `1000000` | `1000` | The table's row cap: a write that would grow the table past it is refused. A read accepts up to twice the cap, so a cap lowered beneath the table leaves it readable, and fails past that, so what a read holds stays bounded |
| `SETTINGS_MAX_TABLE_BYTES` | int, at most `1073741824` | `1048576` | The table's byte cap, counting every row's key and its value's JSON text in bytes, by the row cap's rule: a write that would grow the table past it is refused, and a read accepts up to twice it. The database sends a row's key and value only while the running total of the rows' bytes, in key order, is within twice the cap, so one read holds at most twice the cap in keys and values however large a row another writer stored |
| `SETTINGS_MAX_WRITE_KEYS` | int, at most `1000000` | `SETTINGS_MAX_ROWS` | The most keys one write may name; more is a 422 `too many`, refused before the write waits for its turn. Unset, the row cap |
| `SETTINGS_MAX_KEY_BYTES` | int, at most `2048` | `1024` | The longest key a write may name, in bytes, spelled as it is stored; longer is a 422 `too long`, refused before the write waits for its turn. The bound stays beneath the largest entry PostgreSQL's btree index on `key` takes, about 2.7 KB. A row stored under a longer key, the cap since lowered, is deleted by a `null` at a key above it, or after raising the cap |
| `SETTINGS_MAX_BODY_BYTES` | int | `1048576` | The largest request body; a larger one is a 413 |
| `SETTINGS_READ_HEADER_TIMEOUT`, `SETTINGS_READ_TIMEOUT` | duration | `5s`, `10s` | The server's bounds on reading a request's headers, and the whole request |
| `SETTINGS_WRITE_TIMEOUT` | duration | `15s` | The server's bound on a response; longer than the query timeout |
| `SETTINGS_IDLE_TIMEOUT` | duration | `60s` | How long a keep-alive connection idles |
| `SETTINGS_SHUTDOWN_TIMEOUT` | duration | `10s` | The drain deadline on `SIGTERM` |

A write whose time runs out — waiting for its turn, behind another write of the same replica, for a pooled connection, or for the lock row another replica holds, or once it has it, reading the table, checking the write or storing it — is contention: a 503 with `reason` `busy` and a `Retry-After` of the query timeout, logged at `Warn` as `settings write timed out`. Retrying is safe: a write sets its keys to their values, so a retry after a commit the deadline cut short, which the database may have completed, sets them again. Frequent ones mean writes take too long or arrive too fast for `SETTINGS_QUERY_TIMEOUT`: the entry's error says which step the deadline cut short. Every other 503 is logged at `Error`: `the settings fail to load without this write` means the service's own settings do not load even with the write left out, so no client is at fault. The entry names what to check — `SETTINGS_CONFIG_FILE`, the `APP_*` variables and the settings table — and the wrapped error names the key that fails. Fix the file or the variable and redeploy, or fix or delete the row — a row whose field a release removed is deleted by writing its key as `null`, as is a hand-written row whose key does not parse (`key "limits[x]" does not parse`), by its text — and the next poll recovers; `taking the settings lock` means the lock row is gone — run `settingsstore migrate`, which restores it; anything else is the database. While the settings do not load, every reload fails and the service keeps its previous config. `settings table over its cap`, at `Warn` on every read, means `SETTINGS_MAX_ROWS` or `SETTINGS_MAX_TABLE_BYTES` was lowered beneath the table; the entry gives the table's `rows` and `bytes` beside each cap. The service still loads the table and takes every write that does not grow it past a cap, deletes among them, and refuses one that does as `full`: delete rows through the API until the warning stops. A table past twice a cap fails every read with `the table holds more than N rows` or `more than N bytes`, so reloads keep the previous config, writes fail and restarts fail: raise that cap to at least half the table's size, delete rows through the API, then lower it. Lower a cap by at most half at a time. `settings the schema does not read left out of the load`, at `Warn` on every load that finds one, names in `keys` what a removed field left: write each key as `null` to delete its row, or, for a part of a row's value such as `limits.old` inside the `limits` row, write the row's key whole again.

<!-- recipe: settingsstore/main.go -->

```go
// Sources are how the service loads AppConfig: Options builds the options,
// the table read through entries under the table's source options, and
// Beneath names the sources beneath the table, for an operator to check
// when the settings fail to load.
type Sources struct {
	Options func(entries Entries, table ...config.SourceOption) []config.Option
	Beneath string
}

// appSources layers the table over the file and APP_* variables.
func appSources(file string, logger *slog.Logger) Sources {
	return Sources{
		Options: func(entries Entries, table ...config.SourceOption) []config.Option {
			return []config.Option{
				config.WithFile(file),
				config.WithEnvPrefix("APP"),
				config.WithOverrideEntries(entries, table...),
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
// rows stored beneath it, or, set to nil, deleted with them, and in either
// case cut out of the values of the rows stored above it. A key the
// schema does not read may be set only to nil, to delete the rows a release
// that removed its field left. A write naming more keys than its limits
// allow, or a longer key, is refused before it waits for its turn. Unless
// the table the write leaves loads with the service's options, and fits the
// caps or is no larger than before, the write is refused with a
// *RefusedError and nothing changes. A write whose time runs out, waiting
// for its turn or once it has it, is ErrBusy.
func (s *Settings) Write(ctx context.Context, edits map[string]any) ([]string, error) {
	if len(edits) > s.writeLimits.Keys {
		return nil, &RefusedError{Reason: ReasonTooMany, Keys: []string{}}
	}
	writes, unread, err := canonical[AppConfig](edits, s.writeLimits.KeyBytes)
	if err != nil || len(writes) == 0 && len(unread) == 0 {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	keys, err := s.store(ctx, writes, unread)
	// Whichever step the deadline cut short, the client retries the write,
	// so every one is ErrBusy; a refusal stands, as no retry changes it.
	if _, refused := errors.AsType[*RefusedError](err); err != nil && !refused && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("%w: %w", ErrBusy, err)
	}
	return keys, err
}

// store makes the write in its turn: it waits for this replica's other
// write and for the lock row, then reads the table, checks the table the
// write leaves and commits it.
func (s *Settings) store(ctx context.Context, writes map[string]any, unread []string) ([]string, error) {
	select {
	case s.writing <- struct{}{}:
		defer func() { <-s.writing }()
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for this replica's other settings write: %w", context.Cause(ctx))
	}
	// At READ COMMITTED each statement reads what committed before it, so a
	// write that waited for the lock row reads the table the write before it
	// left; at REPEATABLE READ or SERIALIZABLE, a server default some
	// deployments set, Postgres refuses the waiting write's update instead.
	// SQLite's driver ignores the level: its transactions are serializable,
	// and updating the lock row takes the database's write lock.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("beginning a settings write: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // after Commit, a no-op
	// Updating the lock row is the write's first statement: the database
	// holds the row until the write ends, so writes from every replica take
	// turns, and each reads the table the one before it left.
	locked, err := tx.ExecContext(ctx, "UPDATE settings_lock SET writes = writes + 1 WHERE id = 1")
	if err != nil {
		return nil, fmt.Errorf("taking the settings lock: %w", err)
	}
	n, err := locked.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("taking the settings lock: %w", err)
	}
	if n != 1 {
		return nil, errors.New("taking the settings lock: settings_lock has no row 1; run `settingsstore migrate`")
	}
	stored, err := readRows(ctx, tx, s.limits.read())
	if err != nil {
		return nil, err
	}
	c, err := apply[AppConfig](stored.values, writes, unread)
	if err != nil {
		return nil, err
	}
	keys := append(slices.Collect(maps.Keys(writes)), unread...)
	slices.Sort(keys)
	texts, err := encode(writes, c.rewritten)
	if err != nil {
		return nil, err
	}
	after := tableSize{rows: len(c.table)}
	for key := range c.table {
		if text, ok := texts[key]; ok {
			after.bytes += int64(len(key) + len(text))
		} else {
			after.bytes += stored.sizes[key]
		}
	}
	if s.limits.refuses(stored.size(), after) {
		return nil, &RefusedError{Reason: ReasonFull, Keys: keys}
	}
	if err := s.check(ctx, stored.values, c.table, writes, keys); err != nil {
		return nil, err
	}
	for _, key := range c.deleted {
		if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE key = $1", key); err != nil {
			return nil, fmt.Errorf("deleting %s: %w", key, err)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(texts)) {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = excluded.value",
			key, texts[key]); err != nil {
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
// check loads after, the table a write of writes leaves, with the service's
// options. A table that does not load refuses the write, as does a key no
// field reads inside a value the write sets; one a stored row holds is
// tolerated, as loads tolerate it. The write is not refused when before,
// the table stored now, does not load either: then the fault lies in a
// source beneath the table or in a row already stored, and no client can
// fix it by retrying.
func (s *Settings) check(ctx context.Context, before, after, writes map[string]any, keys []string) error {
	_, err := config.Load[AppConfig](ctx, s.sources.Options(fixed(after), config.WithUnknownEntries(unknownInWrites(writes)))...)
	if err == nil || !errors.Is(err, config.ErrValidation) && !errors.Is(err, config.ErrKey) {
		return err
	}
	if _, storedErr := config.Load[AppConfig](ctx, s.sources.Options(fixed(before), config.WithUnknownEntries(nil))...); storedErr != nil {
		return fmt.Errorf("the settings fail to load without this write; check %s and the settings table: %w", s.sources.Beneath, storedErr)
	}
	reason := ReasonInvalid
	if _, ok := errors.AsType[*writtenUnknownError](err); ok {
		reason = ReasonUnknown
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
