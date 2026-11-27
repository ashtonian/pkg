//go:build unit || integration

package parsetest

import (
	"testing"
	"time"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// Stock keyed by (warehouse_id, sku), its moves and its tags: the resources
// of the composite-key relation tests.
type CrStock struct {
	WarehouseID int64  `json:"warehouse_id" db:"warehouse_id" parse:"filter,sort"`
	SKU         string `json:"sku"          db:"sku"          parse:"filter,sort"`
	Name        string `json:"name"         db:"name"         parse:"filter"`
}

type CrMove struct {
	ID          int64  `json:"id"           db:"id"           parse:"filter,sort"`
	WarehouseID int64  `json:"warehouse_id" db:"warehouse_id" parse:"filter"`
	SKU         string `json:"sku"          db:"sku"          parse:"filter"`
	Code        string `json:"code"         db:"code"         parse:"filter"`
}

type CrTag struct {
	ID    string `json:"id"    db:"id"    parse:"filter,sort"`
	Label string `json:"label" db:"label" parse:"filter"`
}

// Declared relations over one fixture: devices have commands
// (soft-deleted, tenant-scoped), commands have receipts, devices belong to a
// device type (platform types shared, tenant types private: policy-scoped),
// devices and tags pair through device_tags, and assets belong to a parent
// asset (a relation back to its own table).

type RelDevice struct {
	ID        string     `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  string     `json:"tenant_id"  db:"tenant_id"`
	Name      string     `json:"name"       db:"name"       parse:"filter,sort" indexed:"true"`
	TypeID    *string    `json:"type_id"    db:"type_id"    parse:"filter"`
	DeletedAt *time.Time `json:"deleted_at" db:"deleted_at"`
	// Zones is a pair table compared by key (the relation tag).
	Zones []string `json:"-" db:"-" parse:"filter" relation_name:"zones" relation:"device_zones,device_id,zone_id,id,tenant_id" indexed:"true"`
}

type RelCommand struct {
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

type RelAsset struct {
	ID       string  `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID string  `json:"tenant_id" db:"tenant_id"`
	Name     string  `json:"name"      db:"name"      parse:"filter"`
	ParentID *string `json:"parent_id" db:"parent_id" parse:"filter"`
}

type RelResources struct {
	Devices *parse.Resource[RelDevice]
	Assets  *parse.Resource[RelAsset]
}

// RelationResources builds the fixture's resources on p.
func RelationResources(t *testing.T, p *parse.Provider) RelResources {
	t.Helper()
	devices := parse.MustResource[RelDevice](p, "devices", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	commands := parse.MustResource[RelCommand](p, "commands", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	receipts := parse.MustResource[relReceipt](p, "receipts", parse.TenantColumn("tenant_id"))
	types := parse.MustResource[relType](p, "device_types", parse.TenantColumn("tenant_id"))
	tags := parse.MustResource[relTag](p, "tags", parse.TenantColumn("tenant_id"), parse.DeletedColumn("deleted_at"))
	assets := parse.MustResource[RelAsset](p, "assets", parse.TenantColumn("tenant_id"))

	devices.MustRelate("commands", parse.HasMany(commands, "device_id", parse.Indexed(), parse.Includable(), parse.Facetable()))
	devices.MustRelate("type", parse.BelongsTo(types, "type_id", parse.PolicyScoped()))
	devices.MustRelate("tags", parse.ManyToMany(tags, parse.Junction{Table: "device_tags", Source: []string{"device_id"}, Target: []string{"tag_id"}, Tenant: "tenant_id"}, parse.Indexed(), parse.Includable(), parse.Facetable(), parse.Writable()))
	devices.MustRelate("shared_types", parse.ManyToMany(types, parse.Through("device_shared_types", "device_id", "type_id"), parse.PolicyScoped(), parse.Includable(), parse.Facetable(), parse.Writable()))
	commands.MustRelate("device", parse.BelongsTo(devices, "device_id"))
	commands.MustRelate("receipts", parse.HasMany(receipts, "command_id", parse.Indexed()))
	assets.MustRelate("parent", parse.BelongsTo(assets, "parent_id"))
	assets.MustRelate("children", parse.HasMany(assets, "parent_id", parse.Indexed()))
	resources := RelResources{Devices: devices, Assets: assets}
	return resources
}

// RelationScopeT1 is t1's scope: device types it may see are its own and
// the platform's.
func RelationScopeT1() parse.Scope {
	return parse.Scope{Tenant: "t1", Related: map[string]*filter.PolicyConstraints{
		"device_types": {Or: []*filter.PolicyConstraints{
			{Constraints: []*filter.PolicyConstraint{{Field: "tenant_id", Operator: filter.OpEq, Value: "t1"}}},
			{Constraints: []*filter.PolicyConstraint{{Field: "tenant_id", Operator: filter.OpNull}}},
		}},
	}}
}
