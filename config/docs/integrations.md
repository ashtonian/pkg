# Integrations and recipes

[Documentation](README.md) · [Quick start](../README.md#quick-start) · [Go API](https://pkg.go.dev/github.com/ashtonian/pkg/config)

- [Recipes](#recipes)
- [CLI flags](#cli-flags)
- [Decoding a map](#decoding-a-map)
- [Keeping an existing koanf loader](#keeping-an-existing-koanf-loader)

## Recipes

Complete programs, each with a test, in the [cookbook](../examples/cookbook):

- [Kubernetes ConfigMap and Secret volumes](../examples/cookbook/README.md#kubernetes-configmap-and-secret-volumes) — the file watch on a ConfigMap, Secret keys through `k8smount` and its watch.
- [Settings in a database](../examples/cookbook/README.md#settings-in-a-database) — a table read on every reload through `NewMapProvider`.
- [AWS Secrets Manager](../examples/cookbook/README.md#aws-secrets-manager) — `secret` tags resolved per environment.
- [HashiCorp Vault](../examples/cookbook/README.md#hashicorp-vault) — a KV v2 secret as config keys.
- [A whole service](../examples/cookbook/README.md#a-whole-service) — Kong flags, validation, hot reload, appliers, admin endpoints and graceful shutdown.

## CLI flags

[kongprovider](../kongprovider) loads the [Kong](https://github.com/alecthomas/kong) flags a user typed as the highest-precedence source, mapped to keys by the config field each flag writes, and fills Kong's help from the config struct. Embed the config in the command that uses it; `kongprovider.Load` loads it while Kong parses, before Kong runs any `Validate` method, and writes it into that struct, so the command's fields and `Run` see the loaded config:

```go
type ServeCmd struct {
	AppConfig `embed:""`
}

var cli CLI // holds Config (the file's path) and Serve ServeCmd
kctx := kong.Parse(&cli, kongprovider.Load(ctx, &cli.Serve.AppConfig, []string{"APP"},
	func(*kong.Context) ([]config.Option, error) {
		return []config.Option{config.WithFile(cli.Config)}, nil
	}))
kctx.FatalIfErrorf(kctx.Run())
```

`kongprovider.LoadReloader` builds a `Reloader` the same way and binds it for `Run`. Loading after `kong.Parse` with `HelpFromConfig` and `WithOverridesFromStruct` leaves the struct inside the CLI holding Kong's defaults and typed flags — and Kong calls its `Validate` method on those during Parse — so prefer `Load`; see the [kongprovider README](../kongprovider/README.md#wiring-by-hand).

## Decoding a map

`Decode[T](ctx, data, opts...)` runs a map you already hold — a plugin's settings block, a parsed document, a test fixture — through the same pipeline as `Load`: tag defaults beneath it, aliases and separators, strict keys, decode hooks, secrets, post-load hooks and validation. `data` loads as a base provider, so a file, variable or flag that `opts` add loads above it.

```go
plugin, err := config.Decode[PluginConfig](ctx, settings["audit"].(map[string]any))
```

## Keeping an existing koanf loader

A service that already loads with koanf can take the reload lifecycle alone: hand its loader to `NewReloaderFunc` and name the files it reads.

```go
reloader, err := config.NewReloaderFunc(ctx, func(ctx context.Context) (*AppConfig, error) {
	k := koanf.New(".")
	if err := k.Load(file.Provider("/etc/app/config.yaml"), yaml.Parser()); err != nil {
		return nil, err
	}
	var cfg AppConfig
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, err
	}
	return &cfg, cfg.Validate() // return only a complete, valid config
}, config.WithWatchedFiles("/etc/app/config.yaml"), config.WithFileWatch(0))
```
