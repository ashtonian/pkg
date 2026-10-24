//go:build unit

package parse_test

import (
	"fmt"
	"net/url"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

type Device struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	Name     string `json:"name"      db:"name"      parse:"filter,sort,patch"`
	Status   string `json:"status"    db:"status"    parse:"filter,patch" enum:"active,offline"`
}

// A list request — flat and JSON filters, sort, page size — compiled for
// PostgreSQL under the server's tenant: the key is the final keyset column,
// and the page reads one row more than it returns to know there is a next.
func ExampleResource_BuildList() {
	devices := parse.MustResource[Device]("devices", parse.TenantColumn("tenant_id"), parse.PageSize(25, 100))

	q, _ := url.ParseQuery(`status=active&filter={"name":{"$ne":"spare"}}&sort=name&page_size=10`)
	req, err := devices.ParseList(q)
	if err != nil {
		fmt.Println(err)
		return
	}
	plan, err := devices.BuildList(filter.PostgresDialect{}, req, parse.Scope{Tenant: "t1"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(plan.Page.SQL)
	fmt.Println(plan.Page.Args)
	// Output:
	// SELECT devices.id, devices.tenant_id, devices.name, devices.status FROM devices WHERE ((devices.name <> $1 AND devices.status = $2) AND devices.tenant_id = $3) ORDER BY devices.name COLLATE pg_c_utf8 ASC, devices.id COLLATE pg_c_utf8 ASC LIMIT 11
	// [spare active t1]
}

// A merge patch applied to one device, compiled for PostgreSQL under the
// server's tenant: the SET placeholders come first, then the tenant floor and
// the target, and the statement returns the row as the update left it.
func ExampleResource_BuildUpdate() {
	devices := parse.MustResource[Device]("devices", parse.TenantColumn("tenant_id"))

	parsed, err := devices.ParseMergePatch([]byte(`{"status":"offline"}`))
	if err != nil {
		fmt.Println(err)
		return
	}
	plan, err := devices.BuildUpdate(filter.PostgresDialect{}, parsed, parse.ByID("d1"), parse.Scope{Tenant: "t1"})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(plan.Owner.SQL)
	fmt.Println(plan.Owner.Args)
	// Output:
	// UPDATE devices SET status = $1 WHERE (devices.tenant_id = $2 AND devices.id = $3) RETURNING devices.id, devices.tenant_id, devices.name, devices.status
	// [offline t1 d1]
}

type Command struct {
	ID       string `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string `json:"tenant_id" db:"tenant_id"`
	DeviceID string `json:"device_id" db:"device_id" parse:"filter"`
	Status   string `json:"status"    db:"status"    parse:"filter"`
	Attempts int64  `json:"attempts"  db:"attempts"  parse:"filter"`
}

// Devices have commands: a request filters devices by their commands with
// the commands' own filter, every predicate inside one $some on one command,
// held to the tenant.
func ExampleResource_Relate() {
	devices := parse.MustResource[Device]("devices", parse.TenantColumn("tenant_id"))
	commands := parse.MustResource[Command]("commands", parse.TenantColumn("tenant_id"))
	devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Indexed()))
	commands.MustRelate("device", parse.BelongsTo(devices, "device_id"))

	for _, f := range []string{
		`{"commands":{"$some":{"status":"pending","attempts":{"$gt":2}}}}`,
		`{"commands":{"$none":{"status":"failed"}}}`,
	} {
		req, err := devices.ParseList(url.Values{"filter": {f}})
		if err != nil {
			fmt.Println(err)
			return
		}
		plan, err := devices.BuildList(filter.PostgresDialect{}, req, parse.Scope{Tenant: "t1"})
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(plan.Fragment, plan.Page.Args)
	}
	// Output:
	// WHERE (EXISTS (SELECT 1 FROM commands WHERE commands.device_id = devices.id AND commands.tenant_id = $1 AND (commands.attempts > $2 AND commands.status = $3)) AND devices.tenant_id = $4) ORDER BY devices.id COLLATE pg_c_utf8 ASC LIMIT 51 [t1 2 pending t1]
	// WHERE (NOT EXISTS (SELECT 1 FROM commands WHERE commands.device_id = devices.id AND commands.tenant_id = $1 AND commands.status = $2) AND devices.tenant_id = $3) ORDER BY devices.id COLLATE pg_c_utf8 ASC LIMIT 51 [t1 failed t1]
}
