//go:build unit || integration

package parse_test

// tenantKeyCommand is a device's command in a tenant keyed by an integer.
type tenantKeyCommand struct {
	ID       int    `json:"id"        db:"id"        parse:"filter,sort"`
	TenantID int    `json:"-"         db:"tenant_id"`
	DeviceID int    `json:"device_id" db:"device_id" parse:"filter"`
	Status   string `json:"status"    db:"status"    parse:"filter"`
}
