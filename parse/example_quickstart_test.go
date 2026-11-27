//go:build unit

package parse_test

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/parseprovider"
)

type QuickDevice struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	Name     string `json:"name"      db:"name"      parse:"filter,sort,patch"`
	Status   string `json:"status"    db:"status"    parse:"filter,patch" enum:"active,offline"`
}

// The README's quick start: a list request read and a patch applied under
// the server's scope, on the in-memory SQLite database the provider
// settings' defaults name.
func Example_quickStart() {
	ctx := context.Background()
	db, err := parseprovider.Open(ctx, parseprovider.Config{
		Provider:      "sqlite",
		DSN:           ":memory:",
		SettleTimeout: parse.DefaultSettleTimeout,
		SQLite:        parseprovider.SQLiteConfig{MaxOpenConns: 1, BusyTimeout: 5 * time.Second},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = db.Close() }()
	sqlDB, _ := db.SQLite()
	_, err = sqlDB.ExecContext(ctx, `CREATE TABLE devices (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, name TEXT NOT NULL, status TEXT NOT NULL);
		INSERT INTO devices VALUES ('d1', 't1', 'switch', 'active'), ('d2', 't1', 'router', 'active'),
			('d3', 't1', 'spare', 'active'), ('d4', 't1', 'desk', 'offline'), ('d5', 't2', 'core', 'active')`)
	if err != nil {
		fmt.Println(err)
		return
	}

	devices := parse.MustResource[QuickDevice](db.Provider(), "devices", parse.TenantColumn("tenant_id"), parse.PageSize(25, 100))
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
