package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/config"
)

// A write's work while it holds the lock row grows with the table and the
// write, not with their product: each stored key is read once, and the
// write's keys are found by their depth. Twice the rows and twice the keys
// cost about twice as much, not four times.
func TestApply_CostIsTheRowsAndTheKeysNotTheirPairs(t *testing.T) {
	allocs := func(n int) float64 {
		stored, writes := map[string]any{}, map[string]any{}
		var unread []string
		for i := range n {
			stored[fmt.Sprintf(`upstreams["u%d"].url`, i)] = "https://u.example"
			stored[fmt.Sprintf("gone%d", i)] = json.Number("1")
			unread = append(unread, fmt.Sprintf("gone%d", i))
			writes[fmt.Sprintf(`upstreams["w%d"].timeout`, i)] = "2s"
		}
		var err error
		cost := testing.AllocsPerRun(2, func() { _, err = apply[AppConfig](stored, writes, unread) })
		require.NoError(t, err)
		return cost
	}
	small, large := allocs(100), allocs(200)
	t.Logf("allocations for 100 rows and keys, then 200: %.0f, %.0f", small, large)
	assert.Less(t, large, 3*small, "allocations for 100 rows and keys, then 200: %.0f, %.0f", small, large)
}

// body is edits as a request body.
func body(t *testing.T, edits map[string]any) string {
	t.Helper()
	text, err := json.Marshal(edits)
	require.NoError(t, err)
	return string(text)
}

// incompressible is n bytes of hex no compression shortens, from a fixed
// seed: a key PostgreSQL's btree stores at its full length.
func incompressible(n int) string {
	raw := make([]byte, (n+1)/2)
	r := rand.New(rand.NewPCG(1, 2))
	for i := range raw {
		raw[i] = byte(r.Uint32())
	}
	return hex.EncodeToString(raw)[:n]
}

// A write naming more keys than its cap, or a key longer than its cap as it
// is stored, is refused before it waits for its turn: a 422 while another
// replica holds the lock row. A key at the largest cap the boot config takes
// is stored on every backend.
func TestSettings_WriteLimits(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		d := b.fresh(t)
		db := d.open(t)
		require.NoError(t, migrate(t.Context(), db, 10*time.Second))
		const maxKeyBytes = 2048
		limits := WriteLimits{Keys: 2, KeyBytes: maxKeyBytes}
		s := NewSettings(db, d.sources(), 500*time.Millisecond, TableLimits{Rows: 100, Bytes: 1 << 20}, limits, slog.New(slog.DiscardHandler))
		h := settingsHandler(s, func() {}, 1<<20, slog.New(slog.DiscardHandler))
		keyOf := func(n int) string {
			const around = len(`upstreams[""].url`)
			return `upstreams["` + incompressible(n-around) + `"].url`
		}

		atCap := keyOf(maxKeyBytes)
		require.Len(t, atCap, maxKeyBytes)
		rec := put(h, body(t, map[string]any{atCap: "https://a.example", "limits.max": 300}))
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
		assert.Contains(t, rowsOf(t, db), atCap)

		other, _ := replica(t, d, 100)
		tx, err := other.db.BeginTx(t.Context(), nil)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		_, err = tx.Exec("UPDATE settings_lock SET writes = writes + 1 WHERE id = 1")
		require.NoError(t, err)

		pastCap := keyOf(maxKeyBytes + 1)
		for name, tc := range map[string]struct {
			edits map[string]any
			want  RefusedError
		}{
			"too many keys":   {map[string]any{"limits.min": 1, "limits.max": 2, "limits.bytes": 3}, RefusedError{Reason: ReasonTooMany, Keys: []string{}}},
			"too long a key":  {map[string]any{pastCap: "https://b.example"}, RefusedError{Reason: ReasonTooLong, Keys: []string{pastCap}}},
			"too long a null": {map[string]any{"gone" + strings.Repeat("x", maxKeyBytes): nil}, RefusedError{Reason: ReasonTooLong, Keys: []string{"gone" + strings.Repeat("x", maxKeyBytes)}}},
		} {
			rec := put(h, body(t, tc.edits))
			if !assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, name) {
				continue
			}
			var refused RefusedError
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &refused), name)
			assert.Equal(t, tc.want, refused, name)
		}
	})
}

// bootFrom loads BootConfig from vars and a database URL.
func bootFrom(t *testing.T, vars ...string) (*BootConfig, error) {
	t.Helper()
	environ := append([]string{"SETTINGS_DATABASE_URL=postgres://settings@db.example/settings"}, vars...)
	return loadBoot(t.Context(), func() []string { return environ }, slog.New(slog.DiscardHandler))
}

// Every cap has an upper bound: at it the boot config loads, and a read's
// limits, twice the table's caps, are exact; past it the load fails naming
// the variable. A write's key cap left unset is the row cap.
func TestBootConfig_CapsAreBounded(t *testing.T) {
	for _, tc := range []struct {
		key   string
		bound int64
	}{
		{"max_rows", 1_000_000},
		{"max_table_bytes", 1 << 30},
		{"max_write_keys", 1_000_000},
		{"max_key_bytes", 2048},
	} {
		variable := "SETTINGS_" + strings.ToUpper(tc.key)
		_, err := bootFrom(t, fmt.Sprintf("%s=%d", variable, tc.bound))
		require.NoError(t, err, variable)
		_, err = bootFrom(t, fmt.Sprintf("%s=%d", variable, tc.bound+1))
		require.ErrorIs(t, err, config.ErrValidation, variable)
		assert.Contains(t, err.Error(), tc.key+"(max=", variable)
	}

	boot, err := bootFrom(t, "SETTINGS_MAX_ROWS=1000000", "SETTINGS_MAX_TABLE_BYTES=1073741824")
	require.NoError(t, err)
	assert.Equal(t, TableLimits{Rows: 2_000_000, Bytes: 2 << 30}, TableLimits{Rows: boot.MaxRows, Bytes: boot.MaxTableBytes}.read())
	assert.Equal(t, 1_000_000, boot.MaxWriteKeys, "unset, the row cap")

	boot, err = bootFrom(t, "SETTINGS_MAX_ROWS=50", "SETTINGS_MAX_WRITE_KEYS=7")
	require.NoError(t, err)
	assert.Equal(t, 7, boot.MaxWriteKeys)
}

// A read at the caps' upper bounds takes them twice over, exactly, on every
// backend.
func TestReadRows_AtTheCapsBounds(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		s, _ := newReplica(t, b, 100)
		write(t, s, map[string]any{"limits.max": 300})
		boot, err := bootFrom(t, "SETTINGS_MAX_ROWS=1000000", "SETTINGS_MAX_TABLE_BYTES=1073741824")
		require.NoError(t, err)
		stored, err := readRows(t.Context(), s.db, TableLimits{Rows: boot.MaxRows, Bytes: boot.MaxTableBytes}.read())
		require.NoError(t, err)
		assert.Equal(t, tableSize{rows: 1, bytes: int64(len("limits.max") + len("300"))}, stored.size())
	})
}

// A write whose time runs out once its turn came — here in the load that
// checks it — stored nothing and is contention, as one that ran out
// waiting: a 503 with Retry-After, logged at Warn.
func TestSettingsHandler_ADeadlineOnceItsTurnCameIsBusy(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		d := b.fresh(t)
		db := d.open(t)
		require.NoError(t, migrate(t.Context(), db, 10*time.Second))
		waits := Sources{
			Options: func(Entries, ...config.SourceOption) []config.Option {
				return []config.Option{config.WithOverrideEntries(func(ctx context.Context) (map[string]any, error) {
					<-ctx.Done()
					return nil, ctx.Err()
				})}
			},
			Beneath: "a source answering only when the load ends",
		}
		s := NewSettings(db, waits, 200*time.Millisecond, TableLimits{Rows: 100, Bytes: 1 << 20}, testWriteLimits, slog.New(slog.DiscardHandler))
		var logs bytes.Buffer
		h := settingsHandler(s, func() {}, 1<<20, slog.New(slog.NewJSONHandler(&logs, nil)))

		rec := put(h, `{"limits.max": 300}`)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Equal(t, "1", rec.Header().Get("Retry-After"))
		assert.JSONEq(t, `{"reason": "busy"}`, rec.Body.String())
		var entry struct{ Level, Msg string }
		require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
		assert.Equal(t, "WARN", entry.Level, entry.Msg)
		assert.Empty(t, rowsOf(t, db))
	})
}

// A read holds at most its byte limit of keys and values, however large a
// row another writer stored: the database sends neither the key nor the
// value of a row past it.
func TestReadRows_HoldsAtMostItsByteLimit(t *testing.T) {
	onEveryBackend(t, func(t *testing.T, b backend) {
		_, db := newReplica(t, b, 100)
		size := 32 << 20
		if b.name == "postgres" {
			size = 128 << 10 // compressed within the largest entry its btree takes
		}
		_, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('limits.max', '300'), ($1, '1')`, "z"+strings.Repeat("k", size))
		require.NoError(t, err)

		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		_, err = readRows(t.Context(), db, TableLimits{Rows: 10, Bytes: 1024})
		runtime.ReadMemStats(&after)
		assert.ErrorContains(t, err, "more than 1024 bytes")
		assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(size/2), "the read allocated less than half the key it did not take")
	})
}

// A write is logged at Info by how many keys it names, never by the keys,
// whose names may be secrets; at Debug the same entry names them, as
// shownKey shows them. So is a refused write.
func TestSettingsHandler_LogsKeysOnlyAtDebug(t *testing.T) {
	s, _ := newReplica(t, sqliteBackend, 100)
	var logs bytes.Buffer
	level := new(slog.LevelVar)
	h := settingsHandler(s, func() {}, 1<<20, slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: level})))
	logged := func(body string) map[string]any {
		t.Helper()
		logs.Reset()
		put(h, body)
		var entry map[string]any
		require.NoError(t, json.Unmarshal(logs.Bytes(), &entry))
		return entry
	}
	written := `{"limits.max": 300, "upstreams[\"api.internal\"].url": "https://a.example"}`
	refused := `{"limits.min": "lots"}`

	entry := logged(written)
	assert.Equal(t, "settings written", entry["msg"])
	assert.Equal(t, 2.0, entry["key_count"])
	assert.NotContains(t, entry, "keys")
	entry = logged(refused)
	assert.Equal(t, "settings write refused", entry["msg"])
	assert.Equal(t, 1.0, entry["key_count"])
	assert.NotContains(t, entry, "keys")

	level.Set(slog.LevelDebug)
	entry = logged(written)
	assert.Equal(t, []any{"limits.max", `upstreams["api.internal"].url`}, entry["keys"])
	entry = logged(refused)
	assert.Equal(t, []any{"limits.min"}, entry["keys"])
}

type shownSchema struct {
	Name      string            `koanf:"name"`
	Tokens    map[string]string `koanf:"tokens" sensitive:"true"`
	Upstreams map[string]struct {
		URL string `koanf:"url"`
	} `koanf:"upstreams"`
	Tuning struct {
		Workers int            `koanf:"workers"`
		Extra   map[string]any `koanf:",remain"`
	} `koanf:"tuning"`
	Vault struct {
		Addr  string            `koanf:"addr"`
		Paths map[string]string `koanf:",remain" sensitive:"true"`
	} `koanf:"vault"`
}

// A logged key withholds what the config package's diagnostics withhold:
// the entries' names of a sensitive map, under a key it reads or one it
// does not, a remain field's keys among them.
func TestShownKey(t *testing.T) {
	for key, want := range map[string]string{
		"name":                   "name",
		"tokens":                 "tokens",
		`tokens["ghp_live"]`:     "tokens[*]",
		`tokens["ghp_live"].old`: "tokens",
		`upstreams["prod"].url`:  `upstreams["prod"].url`,
		`gone["a"]`:              `gone["a"]`,
		"tuning.cache_size":      "tuning.cache_size",
		`tuning["cache_size"]`:   `tuning["cache_size"]`,
		"tuning.api_token":       "tuning[*]",
		"vault.addr":             "vault.addr",
		"vault.prod_db":          "vault[*]",
		"vault.prod_db.x":        "vault",
	} {
		assert.Equal(t, want, shownKey[shownSchema](key), key)
	}
}

// Every variable the service reads is in the README's table.
func TestREADMEConfigTableNamesEveryVariable(t *testing.T) {
	readme, err := os.ReadFile("../README.md")
	require.NoError(t, err)
	usage, err := config.Usage[BootConfig]("SETTINGS")
	require.NoError(t, err)
	for _, u := range usage {
		assert.True(t, strings.Contains(string(readme), "`"+u.Env+"`"), "the README's table names no %s", u.Env)
	}
}
