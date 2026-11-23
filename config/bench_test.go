package config

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/knadh/koanf/providers/confmap"
)

// The large fixture is a gateway's route table: 256 routes, each with six
// scalars, a four-entry label map and three retry delays — 3,329 values.
type benchRoute struct {
	Name    string            `koanf:"name"`
	Host    string            `koanf:"host"`
	Port    int               `koanf:"port" default:"443"`
	Enabled bool              `koanf:"enabled"`
	Limit   int               `koanf:"limit" default:"50"`
	Timeout int               `koanf:"timeout"`
	Labels  map[string]string `koanf:"labels"`
	Delays  []int             `koanf:"delays"`
}

type benchGateway struct {
	Name   string                `koanf:"name" default:"gateway"`
	Routes map[string]benchRoute `koanf:"routes"`
}

// benchPortGateway is the route table keyed by port, so each entry's key is
// read and named through its key type's codec.
type benchPortGateway struct {
	Name   string             `koanf:"name" default:"gateway"`
	Routes map[int]benchRoute `koanf:"routes"`
}

func benchRouteName(i int) string { return fmt.Sprintf("route-%03d", i) }

func benchRoutePort(i int) string { return strconv.Itoa(8000 + i) }

func benchRoutes(key func(int) string) map[string]any {
	routes := map[string]any{}
	for i := range 256 {
		routes[key(i)] = map[string]any{
			"name": fmt.Sprintf("upstream-%d", i), "host": "service.internal", "port": 8000 + i,
			"enabled": true, "limit": 100, "timeout": 30,
			"labels": map[string]any{"region": "us", "team": "core", "tier": "api", "zone": "a"},
			"delays": []any{1, 2, 4},
		}
	}
	return map[string]any{"routes": routes}
}

func benchGatewayOptions(hooks ...func(context.Context, *benchGateway) error) []Option {
	opts := []Option{
		WithBaseProvider(confmap.Provider(benchRoutes(benchRouteName), ""), nil),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithSkipValidation(),
		WithPostLoad(func(context.Context, *benchGateway) error { return nil }),
	}
	for _, h := range hooks {
		opts = append(opts, WithPostLoad(h))
	}
	return opts
}

func benchPortGatewayOptions() []Option {
	return []Option{
		WithBaseProvider(confmap.Provider(benchRoutes(benchRoutePort), ""), nil),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithSkipValidation(),
	}
}

// The small fixture is a file, the environment and one post-load hook.
func benchSmallOptions(b *testing.B) []Option {
	file := filepath.Join(b.TempDir(), "config.yaml")
	writeBenchFile(b, file, "region: eu\nlabels:\n  a: '1'\n  b: '2'\ntags: [x, y, z]\nitems:\n  one: {port: 1}\n  two: {port: 2}\ndb:\n  password: p\n")
	return []Option{WithFile(file), WithEnvPrefix("APP"), WithEnviron(func() []string { return nil }), WithSkipValidation(),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithPostLoad(func(context.Context, *provCfg) error { return nil })}
}

func BenchmarkLoad(b *testing.B) {
	b.Run("small", func(b *testing.B) { benchLoad[provCfg](b, benchSmallOptions(b)) })
	b.Run("large", func(b *testing.B) { benchLoad[benchGateway](b, benchGatewayOptions()) })
	b.Run("large-port-keyed", func(b *testing.B) { benchLoad[benchPortGateway](b, benchPortGatewayOptions()) })
	// A hook that rewrites every route's name: 256 transformed values.
	b.Run("large-transformed", func(b *testing.B) {
		benchLoad[benchGateway](b, benchGatewayOptions(func(_ context.Context, c *benchGateway) error {
			for k, r := range c.Routes {
				r.Name += "-normalized"
				c.Routes[k] = r
			}
			return nil
		}))
	})
}

func benchLoad[T any](b *testing.B, opts []Option) {
	b.Run("Load", func(b *testing.B) {
		for b.Loop() {
			if _, err := Load[T](context.Background(), opts...); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("LoadSnapshot", func(b *testing.B) {
		for b.Loop() {
			if _, err := LoadSnapshot[T](context.Background(), opts...); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkCanonicalKey resolves a key beneath an entry of a map keyed by
// strings, and of one keyed by integers, whose entry text the key's codec
// reads: as the key's own text, and spelled another way.
func BenchmarkCanonicalKey(b *testing.B) {
	b.Run("string", func(b *testing.B) { benchCanonicalKey[benchGateway](b, `routes["route-001"].port`) })
	b.Run("int", func(b *testing.B) { benchCanonicalKey[benchPortGateway](b, `routes["8001"].port`) })
	b.Run("int-respelled", func(b *testing.B) { benchCanonicalKey[benchPortGateway](b, `routes["0x1f41"].port`) })
}

func benchCanonicalKey[T any](b *testing.B, key string) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := CanonicalKey[T](key); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkReload measures an unchanged reload — a poll that finds nothing
// to do — with provenance, an applier owning the bulk of the config, both,
// or neither.
func BenchmarkReload(b *testing.B) {
	b.Run("small", func(b *testing.B) { benchReload[provCfg](b, benchSmallOptions(b), "items") })
	b.Run("large", func(b *testing.B) { benchReload[benchGateway](b, benchGatewayOptions(), "routes") })
}

func benchReload[T any](b *testing.B, opts []Option, owned string) {
	for _, mode := range []string{"plain", "provenance", "applier", "both"} {
		b.Run(mode, func(b *testing.B) {
			var ro []ReloadOption
			if mode == "provenance" || mode == "both" {
				ro = append(ro, WithProvenance())
			}
			if mode == "applier" || mode == "both" {
				ro = append(ro, WithApplier(Applier[T]{Name: "owner", Prefixes: []string{owned},
					Apply: func(context.Context, ApplyEvent[T]) (ApplyResult, error) { return ApplyResult{}, nil }}))
			}
			r, err := NewReloader[T](context.Background(), opts, ro...)
			if err != nil {
				b.Fatal(err)
			}
			defer func() { _ = r.Stop(context.Background()) }()
			for b.Loop() {
				if _, err := r.Reload(context.Background()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkDiff(b *testing.B) {
	load := func() *benchGateway {
		cfg, err := Load[benchGateway](context.Background(), benchGatewayOptions()...)
		if err != nil {
			b.Fatal(err)
		}
		return cfg
	}
	a, c := load(), load()
	b.Run("equal", func(b *testing.B) {
		for b.Loop() {
			if _, err := Diff(a, c); err != nil {
				b.Fatal(err)
			}
		}
	})
	changed := load()
	for k, r := range changed.Routes {
		r.Limit++
		changed.Routes[k] = r
	}
	b.Run("every-route", func(b *testing.B) {
		for b.Loop() {
			if _, err := Diff(a, changed); err != nil {
				b.Fatal(err)
			}
		}
	})
}
