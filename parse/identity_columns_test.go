//go:build unit

package parse_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// Each declaration below compiles statements no row can be served by, or
// that move a row out of its tenant; NewResource refuses it, naming the
// column or field.
func TestNewResource_RefusesIdentityDeclarations(t *testing.T) {
	type patchedKey struct {
		ID   string `json:"id"   db:"id"   parse:"filter,patch"`
		Name string `json:"name" db:"name" parse:"patch"`
	}
	type patchedTenant struct {
		ID       string `json:"id"        db:"id"`
		TenantID string `json:"tenant_id" db:"tenant_id" parse:"filter,patch"`
	}
	type serverKey struct {
		ID   string `json:"id"   db:"id"   parse:"server"`
		Name string `json:"name" db:"name" parse:"patch"`
	}
	type softDeleted struct {
		ID        string          `json:"id"         db:"id"`
		TenantID  string          `json:"tenant_id"  db:"tenant_id"`
		Deleted   bool            `json:"deleted"    db:"deleted"`
		N         int             `json:"n"          db:"n"`
		DeletedAt *time.Time      `json:"deleted_at" db:"deleted_at"`
		Ref       uuid.UUID       `json:"ref"        db:"ref"`
		Amount    decimal.Decimal `json:"amount"     db:"amount"`
	}
	type immutablePatch struct {
		ID   string `json:"id"   db:"id"`
		Slug string `json:"slug" db:"slug" parse:"filter,patch" immutable:"true"`
	}
	for _, tc := range []struct {
		name  string
		build func() error
		want  string
	}{
		{"a patched key", func() error { _, err := parse.NewResource[patchedKey]("docs"); return err },
			`parse: docs: patch path /id: writes the key column "id"`},
		{"a patched tenant column", func() error {
			_, err := parse.NewResource[patchedTenant]("docs", parse.TenantColumn("tenant_id"))
			return err
		}, `parse: docs: patch path /tenant_id: writes the tenant column "tenant_id"`},
		{"a server-patched key", func() error { _, err := parse.NewResource[serverKey]("docs"); return err },
			`parse: docs: patch path /id: writes the key column "id"`},
		{"the key as the deleted column", func() error {
			_, err := parse.NewResource[softDeleted]("odds", parse.DeletedColumn("id"))
			return err
		}, `parse: odds: deleted column "id" is a key column`},
		{"the tenant column as the deleted column", func() error {
			_, err := parse.NewResource[softDeleted]("odds", parse.TenantColumn("tenant_id"), parse.DeletedColumn("tenant_id"))
			return err
		}, `parse: odds: deleted column "tenant_id" is the tenant column`},
		{"a bool deleted column", func() error {
			_, err := parse.NewResource[softDeleted]("odds", parse.DeletedColumn("deleted"))
			return err
		}, `parse: odds: deleted column "deleted" is bool, which cannot hold NULL`},
		{"an int deleted column", func() error {
			_, err := parse.NewResource[softDeleted]("odds", parse.DeletedColumn("n"))
			return err
		}, `parse: odds: deleted column "n" is int, which cannot hold NULL`},
		// Each is an sql.Scanner that never holds NULL: the UUID scans NULL
		// as the nil UUID it writes back, and the decimal refuses it.
		{"a UUID deleted column", func() error {
			_, err := parse.NewResource[softDeleted]("odds", parse.DeletedColumn("ref"))
			return err
		}, `parse: odds: deleted column "ref" is uuid.UUID, which cannot hold NULL`},
		{"a decimal deleted column", func() error {
			_, err := parse.NewResource[softDeleted]("odds", parse.DeletedColumn("amount"))
			return err
		}, `parse: odds: deleted column "amount" is decimal.Decimal, which cannot hold NULL`},
		{"an immutable patched field", func() error { _, err := parse.NewResource[immutablePatch]("imms"); return err },
			`field Slug: an immutable field cannot have the patch capability`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.ErrorContains(t, tc.build(), tc.want)
		})
	}
}

func TestNewResource_AcceptsWhatARowCanBeServedBy(t *testing.T) {
	type nullable struct {
		ID        string              `json:"id"         db:"id"`
		TenantID  string              `json:"tenant_id"  db:"tenant_id" parse:"filter"`
		At        *time.Time          `json:"at"         db:"at"`
		NullTime  sql.NullTime        `json:"null_time"  db:"null_time"`
		Generic   sql.Null[time.Time] `json:"generic"    db:"generic"`
		Slug      string              `json:"slug"       db:"slug" parse:"filter,server" immutable:"true"`
		DeletedAt *time.Time          `json:"deleted_at" db:"deleted_at" parse:"patch"`
	}
	for _, deleted := range []string{"at", "null_time", "generic"} {
		_, err := parse.NewResource[nullable]("rows", parse.TenantColumn("tenant_id"), parse.DeletedColumn(deleted))
		assert.NoError(t, err, deleted)
	}

	type tenantKeyed struct {
		TenantID string `json:"tenant_id" db:"tenant_id" parse:"filter"`
		Name     string `json:"name"      db:"name"      parse:"patch"`
	}
	_, err := parse.NewResource[tenantKeyed]("tenants", parse.PrimaryKey("tenant_id"), parse.TenantColumn("tenant_id"))
	assert.NoError(t, err, "a resource whose tenant column is its key")

	r, err := parse.NewResource[nullable]("rows", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	require.NoError(t, err)
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, r))
	doc := reg.SchemaFor("rows")
	require.NotNil(t, doc.Patch)
	assert.NotContains(t, doc.Patch.Paths, "/slug", "an immutable field is never a request patch path")
	assert.Contains(t, doc.Patch.Paths, "/deleted_at", "a request may soft-delete a row the patch schema lists")
	_, err = r.ParseServerMergePatch([]byte(`{"slug":"rotated"}`))
	assert.NoError(t, err, "the server may write an immutable field")
}

// A soft-delete column of a nullable wrapper holds the wrapper's value: its
// system field is a timestamp, and Evaluate admits a row under the floor
// while its sql.NullTime is NULL, as SQL's IS NULL does.
func TestDeletedColumn_AWrapperRowIsLiveWhileNull(t *testing.T) {
	type note struct {
		ID        string       `json:"id"         db:"id"`
		DeletedAt sql.NullTime `json:"deleted_at" db:"deleted_at"`
	}
	r, err := parse.NewResource[note]("notes", parse.DeletedColumn("deleted_at"))
	require.NoError(t, err)
	schema := r.FilterSchema()
	assert.Equal(t, core.FieldTypeTimestamp, schema.SystemFields["deleted_at"].DataType)
	floor, err := filter.ApplyPolicyConstraints(schema, nil, filter.ComposeConstraints(filter.RepositoryScope{DeleteColumn: r.DeletedColumnName()}, nil, nil))
	require.NoError(t, err)
	for _, tt := range []struct {
		name      string
		deletedAt sql.NullTime
		want      filter.Truth
	}{
		{"live", sql.NullTime{}, filter.True},
		{"deleted", sql.NullTime{Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Valid: true}, filter.False},
	} {
		t.Run(tt.name, func(t *testing.T) {
			truth, err := filter.Evaluate(floor.Root, filter.ColumnRow(func(column string) (any, bool) {
				return tt.deletedAt, column == "notes.deleted_at"
			}))
			require.NoError(t, err)
			assert.Equal(t, tt.want, truth)
		})
	}
}

// Both update builders hold a write to the key or the tenant column to one
// rule: a patch never names one (NewResource refuses the path), and a server
// column that does is refused by both alike.
func TestUpdateBuilders_RefuseIdentityWritesAlike(t *testing.T) {
	type versioned struct {
		ID       int64  `json:"id"        db:"id"        parse:"filter"`
		TenantID string `json:"tenant_id" db:"tenant_id"`
		Name     string `json:"name"      db:"name"      parse:"patch"`
		Version  int64  `json:"version"   db:"version"   parse:"server"`
	}
	r, err := parse.NewResource[versioned]("versioned", parse.TenantColumn("tenant_id"))
	require.NoError(t, err)
	scope := parse.Scope{Tenant: "t1"}
	for _, tc := range []struct {
		name   string
		body   string
		server bool
		opts   []parse.UpdateOption
		refuse string
	}{
		{"a column", `{"name":"n"}`, false, nil, ""},
		{"a counter", `{"name":"n"}`, false, []parse.UpdateOption{parse.Increment("version")}, ""},
		{"a server column", `{"version":7}`, true, nil, ""},
		{"a counter on the key", `{"name":"n"}`, false, []parse.UpdateOption{parse.Increment("id")}, "cannot change identity column id"},
		{"a stamp on the tenant column", `{"name":"n"}`, false, []parse.UpdateOption{parse.StampSQL("tenant_id", "'t2'")}, "cannot change identity column tenant_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := r.ParseMergePatch([]byte(tc.body))
			if tc.server {
				parsed, err = r.ParseServerMergePatch([]byte(tc.body))
			}
			require.NoError(t, err)
			_, single := r.BuildUpdate(filter.PostgresDialect{}, parsed, parse.ByID(int64(1)), scope, tc.opts...)
			_, batch := r.BuildUpdateBatch(filter.PostgresDialect{}, []parse.RowUpdate{{Key: parse.Key{int64(1)}, Patch: parsed}}, scope, parse.UpdateBatchLimits{}, tc.opts...)
			if tc.refuse == "" {
				assert.NoError(t, single)
				assert.NoError(t, batch)
				return
			}
			assert.ErrorContains(t, single, "versioned: update "+tc.refuse)
			assert.ErrorContains(t, batch, "versioned: update batch "+tc.refuse)
		})
	}
}
