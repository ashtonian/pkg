//go:build unit

package parse_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

type credentialRow struct {
	ID          string    `json:"id" db:"id"`
	Name        string    `json:"name" db:"name" parse:"filter,patch"`
	Fingerprint *string   `json:"fingerprint" db:"fingerprint" parse:"filter,server"`
	HasSecret   bool      `json:"-" db:"has_secret" parse:"server"`
	CreatedAt   time.Time `json:"created_at" db:"created_at" parse:"filter"`
}

// A request body cannot reach a server field; the server's own patch can,
// hidden ones included, and the repositories apply it like any other.
func TestResource_ServerMergePatch(t *testing.T) {
	p, err := parse.NewResource[credentialRow]("credential_rows")
	require.NoError(t, err)

	for _, body := range []string{`{"fingerprint":"abc"}`, `{"has_secret":true}`} {
		_, parseErr := p.ParseMergePatch([]byte(body))
		assert.Error(t, parseErr, "a request cannot set a server field: %s", body)
	}
	_, err = p.ParseServerMergePatch([]byte(`{"created_at":"2026-01-01T00:00:00Z"}`))
	assert.Error(t, err, "the server patch admits server fields, not every column")
	_, err = p.ParseFilter(`{"has_secret":true}`)
	assert.Error(t, err, "a hidden server field is not filterable")

	parsed, err := p.ParseServerMergePatch([]byte(`{"name":"rotated","fingerprint":"abc","has_secret":true}`))
	require.NoError(t, err)

	got, err := p.ApplyValidatedPatch(parsed, credentialRow{ID: "1", Name: "old"})
	require.NoError(t, err)
	require.NotNil(t, got.Fingerprint)
	assert.Equal(t, credentialRow{ID: "1", Name: "rotated", Fingerprint: got.Fingerprint, HasSecret: true}, got)
	assert.Equal(t, "abc", *got.Fingerprint)

	sql, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
	require.NoError(t, err)
	for _, col := range []string{"name", "fingerprint", "has_secret"} {
		assert.Contains(t, sql.SetClause, col)
	}
}
