# Secrets

[Documentation](README.md) · [Quick start](../README.md#quick-start) · [Go API](https://pkg.go.dev/github.com/ashtonian/pkg/config)

Tag a field with where its value lives and give the load a fetch function for your store. `SecretsFrom` sets every `secret` field to what the fetch returns, decoded into the field's type, after decoding and before post-load hooks and validation:

```go
type DBConfig struct {
	Host     string `koanf:"host" default:"localhost"`
	User     string `koanf:"user" secret:"prod/db#username"`
	Password string `koanf:"password" secret:"prod/db#password"`
	APIKey   string `koanf:"api_key" secret:"prod/api-key,optional"`
}

cfg, err := config.Load[AppConfig](ctx,
	config.WithFile("config.yaml"),
	config.WithSecretPopulator(config.SecretsFrom(func(ctx context.Context, ref string) (string, error) {
		out, err := sm.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: &ref})
		var missing *types.ResourceNotFoundException
		if errors.As(err, &missing) {
			return "", fmt.Errorf("%w: %s", config.ErrSecretNotFound, ref)
		}
		if err != nil {
			return "", err
		}
		return aws.ToString(out.SecretString), nil
	})),
)
```

- `ref#field` reads one member of a JSON-object secret; each ref is fetched once per load, with the load's context.
- `,optional` keeps what the sources set when the fetch reports `ErrSecretNotFound` — a development file can supply what production keeps in the store.
- A failure fails the load with `ErrSecretProvider`, naming the key and the ref. A value that does not decode is reported by type, never quoted.
- Under a reloader the fetch runs on every reload, so a poll picks up rotations; a store outage fails that reload and keeps the previous config live.

`WithSecretPopulator` takes any `func(context.Context, any) error` when `SecretsFrom` does not fit.
