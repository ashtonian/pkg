//go:build unit || integration

package parsetest

import (
	"github.com/ashtonian/pkg/parse"
)

// The graph fixtures: devices → commands → logs, each tenant column typed K
// or absent, and tags paired with devices through device_tags.
type GraphDevice[K any] struct {
	ID       int    `json:"id"   db:"id"   parse:"filter,sort"`
	TenantID K      `json:"-"    db:"tenant_id"`
	Name     string `json:"name" db:"name" parse:"filter,sort,patch"`
}

type GraphCommand[K any] struct {
	ID       int    `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID K      `json:"-"         db:"tenant_id"`
	DeviceID int    `json:"device_id" db:"device_id" parse:"filter"`
	Status   string `json:"status"    db:"status"    parse:"filter"`
}

type GraphLog[K any] struct {
	ID        int    `json:"id"         db:"id"         parse:"filter,sort"`
	TenantID  K      `json:"-"          db:"tenant_id"`
	CommandID int    `json:"command_id" db:"command_id" parse:"filter"`
	Level     string `json:"level"      db:"level"      parse:"filter"`
}

type UntenantedDevice struct {
	ID   int    `json:"id"   db:"id"   parse:"filter,sort"`
	Name string `json:"name" db:"name" parse:"filter,sort,patch"`
}

type UntenantedCommand struct {
	ID       int    `json:"id"        db:"id"        parse:"filter,sort"`
	DeviceID int    `json:"device_id" db:"device_id" parse:"filter"`
	Status   string `json:"status"    db:"status"    parse:"filter"`
}

type UntenantedTag struct {
	ID    int    `json:"id"    db:"id"    parse:"filter,sort"`
	Label string `json:"label" db:"label" parse:"filter"`
}

const DepthTwoFilter = `{"commands":{"$some":{"logs":{"$some":{"level":"error"}}}}}`

func Tenanted[T any](p *parse.Provider, table string) *parse.Resource[T] {
	return parse.MustResource[T](p, table, parse.TenantColumn("tenant_id"))
}

// TenantKeyCommand is a device's command in a tenant keyed by an integer.
type TenantKeyCommand struct {
	ID       int    `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID int    `json:"-"         db:"tenant_id"`
	DeviceID int    `json:"device_id" db:"device_id" parse:"filter"`
	Status   string `json:"status"    db:"status"    parse:"filter"`
}
