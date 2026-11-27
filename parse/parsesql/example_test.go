//go:build unit

package parsesql_test

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/parsesql"
)

type Device struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	Name     string `json:"name"      db:"name"      parse:"filter,sort,patch"`
	Status   string `json:"status"    db:"status"    parse:"filter,patch" enum:"active,offline"`
}

type Tag struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Label     string     `json:"label"      db:"label"      parse:"filter"`
	DeletedAt *time.Time `json:"-"          db:"deleted_at"`
}

// The parse README's quick start: a list request read and a patch applied
// under the server's scope, on the in-memory SQLite database Config's
// defaults name.
func Example_quickStart() {
	ctx := context.Background()
	db, err := parsesql.Open(ctx, parsesql.Config{
		DSN:           ":memory:",
		MaxOpenConns:  1,
		BusyTimeout:   5 * time.Second,
		SettleTimeout: parse.DefaultSettleTimeout,
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = db.Close() }()
	_, err = db.DB().ExecContext(ctx, `CREATE TABLE devices (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, name TEXT NOT NULL, status TEXT NOT NULL);
		INSERT INTO devices VALUES ('d1', 't1', 'switch', 'active'), ('d2', 't1', 'router', 'active'),
			('d3', 't1', 'spare', 'active'), ('d4', 't1', 'desk', 'offline'), ('d5', 't2', 'core', 'active')`)
	if err != nil {
		fmt.Println(err)
		return
	}

	devices := parse.MustResource[Device](db.Provider(), "devices", parse.TenantColumn("tenant_id"), parse.PageSize(25, 100))
	scope := parse.Scope{Tenant: "t1"}

	q, _ := url.ParseQuery(`status=active&filter={"name":{"$ne":"spare"}}&sort=name&page_size=10`)
	req, err := devices.ParseList(q)
	if err != nil {
		fmt.Println(err)
		return
	}
	page, err := devices.List(ctx, req, scope)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, d := range page.Items {
		fmt.Println(d.ID, d.Name)
	}

	patch, err := devices.ParseMergePatch([]byte(`{"status":"offline"}`))
	if err != nil {
		fmt.Println(err)
		return
	}
	res, err := devices.Update(ctx, patch, parse.ByID("d1"), scope)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(res.Changed[0].ID, res.Changed[0].Status)

	// d5 is another tenant's: the scope does not reach it, so it is not found.
	other, err := devices.Update(ctx, patch, parse.ByID("d5"), scope)
	fmt.Println(len(other.Changed)+len(other.Unchanged), err)
	// Output:
	// d2 router
	// d1 switch
	// d1 offline
	// 0 <nil>
}

// A membership change pairs only owners and targets the request may relate:
// its INSERT selects both rows, live and in the tenant, rather than binding
// raw keys, so of red (live, in t1), blue (deleted) and green (in t2) the
// update pairs d1 with red alone.
func Example_updateMembership() {
	ctx := context.Background()
	db, err := parsesql.Open(ctx, parsesql.Config{
		DSN:           ":memory:",
		MaxOpenConns:  1,
		BusyTimeout:   5 * time.Second,
		SettleTimeout: parse.DefaultSettleTimeout,
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = db.Close() }()
	for _, stmt := range []string{
		`CREATE TABLE devices (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, name TEXT NOT NULL, status TEXT NOT NULL)`,
		`CREATE TABLE tags (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, label TEXT NOT NULL, deleted_at TEXT)`,
		`CREATE TABLE device_tags (device_id TEXT NOT NULL, tag_id TEXT NOT NULL, tenant_id TEXT NOT NULL, PRIMARY KEY (device_id, tag_id))`,
		`INSERT INTO devices VALUES ('d1', 't1', 'sensor', 'active')`,
		`INSERT INTO tags VALUES ('red', 't1', 'Red', NULL), ('blue', 't1', 'Blue', '2026-01-01T00:00:00.000000000Z'), ('green', 't2', 'Green', NULL)`,
	} {
		_, err := db.DB().ExecContext(ctx, stmt)
		if err != nil {
			fmt.Println(err)
			return
		}
	}
	devices := parse.MustResource[Device](db.Provider(), "devices", parse.TenantColumn("tenant_id"))
	tags := parse.MustResource[Tag](db.Provider(), "tags", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	devices.MustRelate("tags", parse.ManyToMany(tags, parse.Junction{Table: "device_tags", Source: []string{"device_id"}, Target: []string{"tag_id"}, Tenant: "tenant_id"}, parse.Writable()))

	parsed, err := devices.ParsePatch([]byte(`[{"op":"add","path":"/tags","value":["red","blue","green"]}]`))
	if err != nil {
		fmt.Println(err)
		return
	}
	res, err := devices.Update(ctx, parsed, parse.ByID("d1"), parse.Scope{Tenant: "t1"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("pairs written:", res.RelationRows)
	var device, tag, tenant string
	err = db.DB().QueryRowContext(ctx, `SELECT device_id, tag_id, tenant_id FROM device_tags`).Scan(&device, &tag, &tenant)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(device, tag, tenant)
	// Output:
	// pairs written: 1
	// d1 red t1
}
