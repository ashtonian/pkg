//go:build unit

package parse_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
	"github.com/ashtonian/pkg/parse/internal/providertest"
	"github.com/ashtonian/pkg/parse/parsesql"
)

// A relation joins tables of one database: its target is built on the
// owner's provider, not on another of any kind, and a refused relation
// leaves the owner as it was.
func TestRelate_RefusesATargetOnAnotherProvider(t *testing.T) {
	for name, target := range map[string]*parse.Provider{
		"another kind of provider":   onSQLite,
		"another database of a kind": providertest.Compile(filter.PostgresDialect{}),
	} {
		t.Run(name, func(t *testing.T) {
			devices := parse.MustResource[parsetest.UntenantedDevice](onPostgres, "devices")
			commands := parse.MustResource[parsetest.UntenantedCommand](target, "commands")
			err := devices.Relate("commands", parse.HasMany(commands, "device_id"))
			require.ErrorIs(t, err, parse.ErrProviderMismatch)
			require.ErrorContains(t, err, `parse: devices: relation "commands": target commands`)
			assert.NotContains(t, devices.FilterSchema().Fields, "commands")

			own := parse.MustResource[parsetest.UntenantedCommand](onPostgres, "commands")
			require.NoError(t, devices.Relate("commands", parse.HasMany(own, "device_id")))
		})
	}
}

// A resource runs only in a transaction of its kind of provider, whether
// the provider began it or it is the application's own, and not in one
// another provider of its kind began.
func TestIn_RefusesATransactionOfAnotherProvider(t *testing.T) {
	devices := parse.MustResource[parsetest.UntenantedDevice](onPostgres, "devices")
	_, err := devices.In(nil)
	require.ErrorContains(t, err, "parse: devices: nil transaction")

	for name, begin := range map[string]func(t *testing.T, p *parse.Provider, db *sql.DB) *parse.Tx{
		"begun by the provider": func(t *testing.T, p *parse.Provider, _ *sql.DB) *parse.Tx {
			t.Helper()
			tx, err := p.Begin(t.Context())
			require.NoError(t, err)
			return tx
		},
		"the application's own": func(t *testing.T, _ *parse.Provider, db *sql.DB) *parse.Tx {
			t.Helper()
			sqlTx, err := db.BeginTx(t.Context(), nil)
			require.NoError(t, err)
			return parsesql.Tx(sqlTx)
		},
	} {
		t.Run(name, func(t *testing.T) {
			p, db := providertest.SQLite(t)
			tx := begin(t, p, db)
			defer func() { assert.NoError(t, tx.Rollback(t.Context())) }()

			_, err := devices.In(tx)
			require.ErrorIs(t, err, parse.ErrProviderMismatch)
			require.ErrorContains(t, err, "parse: devices: sqlite transaction for a resource on postgres")

			stored := parse.MustResource[parsetest.UntenantedDevice](p, "devices")
			_, err = stored.In(tx)
			require.NoError(t, err)
		})
	}

	t.Run("begun by another provider of its kind", func(t *testing.T) {
		p, _ := providertest.SQLite(t)
		other, _ := providertest.SQLite(t)
		tx, err := other.Begin(t.Context())
		require.NoError(t, err)
		defer func() { assert.NoError(t, tx.Rollback(t.Context())) }()

		_, err = parse.MustResource[parsetest.UntenantedDevice](p, "devices").In(tx)
		require.ErrorIs(t, err, parse.ErrProviderMismatch)
		assert.ErrorContains(t, err, "parse: devices: transaction another sqlite provider began")
	})
}
