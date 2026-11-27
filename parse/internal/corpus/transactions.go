//go:build integration

package corpus

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
)

// A resource runs in a transaction its provider began, or in the
// application's own wrapped by the provider package (Engine.CallerTx), and
// is then on its provider; it refuses one another provider of its kind
// began. A transaction of another kind of provider is refused without a
// database, in the root package's tests.
func inTransactionsOfItsProvider(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		t.Run("begun by the provider", func(t *testing.T) {
			p, _ := b.open(t)
			r := parsetest.ExecItems(p)
			tx, err := p.Begin(t.Context())
			require.NoError(t, err)
			defer func() { require.NoError(t, tx.Rollback(t.Context())) }()
			scoped, err := r.In(tx)
			require.NoError(t, err)
			assert.Same(t, p, scoped.Provider())
		})

		t.Run("the application's own", func(t *testing.T) {
			p, db := b.open(t)
			r := parsetest.ExecItems(p)
			callerTx(t, b, db, func(tx *parse.Tx, _ inTx) {
				scoped, err := r.In(tx)
				require.NoError(t, err)
				assert.Same(t, p, scoped.Provider())
			})
		})

		t.Run("begun by another provider of its kind", func(t *testing.T) {
			p, _ := b.open(t)
			other, _ := b.open(t)
			tx, err := other.Begin(t.Context())
			require.NoError(t, err)
			defer func() { require.NoError(t, tx.Rollback(t.Context())) }()
			_, err = parsetest.ExecItems(p).In(tx)
			require.ErrorIs(t, err, parse.ErrProviderMismatch)
			assert.ErrorContains(t, err, "parse: exec_items: transaction another "+string(b.engine)+" provider began")
		})
	})
}
