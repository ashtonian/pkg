//go:build unit

package parsepgx

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
)

// unsent is an error pgx reports for a statement it never sent.
type unsent struct{}

func (unsent) Error() string     { return "conn closed before the statement was sent" }
func (unsent) SafeToRetry() bool { return true }

func TestCommitFailed_SaysWhetherTheCommitMayHaveTaken(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		unknown bool
	}{
		{"refused by the server", &pgconn.PgError{Severity: "ERROR", SeverityUnlocalized: "ERROR", Code: "23505"}, false},
		{"the commit of an aborted transaction", pgx.ErrTxCommitRollback, false},
		{"never sent", unsent{}, false},
		{"a session the server ended", &pgconn.PgError{Severity: "FATAL", SeverityUnlocalized: "FATAL", Code: "57P01"}, true},
		{"the connection lost in flight", io.ErrUnexpectedEOF, true},
		{"the settle timeout run out in flight", context.DeadlineExceeded, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := commitFailed(tc.err)
			require.ErrorIs(t, err, tc.err)
			failed, ok := errors.AsType[*parse.StatementError](err)
			require.True(t, ok, "%v", err)
			assert.Equal(t, "commit", failed.Statement)
			assert.Equal(t, tc.unknown, errors.Is(err, parse.ErrCommitUnknown))
		})
	}
	require.NoError(t, commitFailed(nil))
}
