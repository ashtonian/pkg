//go:build unit || integration

package parse_test

import (
	"testing"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// Declared relations over one fixture: devices have commands
// (soft-deleted, tenant-scoped), commands have receipts, devices belong to a
// device type (platform types shared, tenant types private: policy-scoped),
// devices and tags pair through device_tags, and assets belong to a parent
// asset (a relation back to its own table).

type relDevice struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Name      string     `json:"name"       db:"name"       parse:"filter,sort" indexed:"true"`
	TypeID    *string    `json:"type_id"    db:"type_id"    parse:"filter"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
	// Zones is a pair table compared by key (the relation tag).
	Zones []string `json:"-" db:"-" parse:"filter" relation_name:"zones" relation:"device_zones,device_id,zone_id,id,tenant_id" indexed:"true"`
}

type relCommand struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	DeviceID  string     `json:"device_id"  db:"device_id"  parse:"filter"`
	Status    string     `json:"status"     db:"status"     parse:"filter"`
	Attempts  int64      `json:"attempts"   db:"attempts"   parse:"filter"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
}

type relReceipt struct {
	ID        string `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string `json:"tenant_id"  db:"tenant_id"`
	CommandID string `json:"command_id" db:"command_id" parse:"filter"`
	OK        bool   `json:"ok"         db:"ok"         parse:"filter"`
}

type relType struct {
	ID       string  `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID *string `json:"tenant_id" db:"tenant_id" parse:"filter"`
	Name     string  `json:"name"      db:"name"      parse:"filter"`
}

type relTag struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Label     string     `json:"label"      db:"label"      parse:"filter"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
}

type relAsset struct {
	ID       string  `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string  `json:"tenant_id" db:"tenant_id"`
	Name     string  `json:"name"      db:"name"      parse:"filter"`
	ParentID *string `json:"parent_id" db:"parent_id" parse:"filter"`
}

type relResources struct {
	devices *parse.Resource[relDevice]
	assets  *parse.Resource[relAsset]
}

// relationResources builds the fixture's resources on p.
func relationResources(t *testing.T, p *parse.Provider) relResources {
	t.Helper()
	devices := parse.MustResource[relDevice](p, "devices", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	commands := parse.MustResource[relCommand](p, "commands", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	receipts := parse.MustResource[relReceipt](p, "receipts", parse.TenantColumn("tenant_id"))
	types := parse.MustResource[relType](p, "device_types", parse.TenantColumn("tenant_id"))
	tags := parse.MustResource[relTag](p, "tags", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	assets := parse.MustResource[relAsset](p, "assets", parse.TenantColumn("tenant_id"))

	devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Indexed(), parse.Includable(), parse.Facetable()))
	devices.MustRelate("type", parse.BelongsTo(types, "type_id", parse.PolicyScoped()))
	devices.MustRelate("tags", parse.ManyToMany(tags, parse.Junction{Table: "device_tags", Source: []string{"device_id"}, Target: []string{"tag_id"}, Tenant: "tenant_id"}, parse.Indexed(), parse.Includable(), parse.Facetable(), parse.Writable()))
	devices.MustRelate("shared_types", parse.ManyToMany(types, parse.Through("device_shared_types", "device_id", "type_id"), parse.PolicyScoped(), parse.Includable(), parse.Facetable(), parse.Writable()))
	commands.MustRelate("device", parse.BelongsTo(devices, "device_id"))
	commands.MustRelate("receipts", parse.HasMany(receipts, "command_id", parse.Indexed()))
	assets.MustRelate("parent", parse.BelongsTo(assets, "parent_id"))
	assets.MustRelate("children", parse.HasMany(assets, "parent_id", parse.Indexed()))
	return relResources{devices: devices, assets: assets}
}

// relationScopeT1 is t1's scope: device types it may see are its own and
// the platform's.
func relationScopeT1() parse.Scope {
	return parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{
		"device_types": {Or: []*filter.PolicyConstraints{
			{Constraints: []*filter.PolicyConstraint{{Field: "tenant_id", Operator: filter.OpEq, Value: "t1"}}},
			{Constraints: []*filter.PolicyConstraint{{Field: "tenant_id", Operator: filter.OpNull}}},
		}},
	}}
}
