//go:build unit || integration

package parsetest

import (
	"github.com/shopspring/decimal"

	"github.com/ashtonian/pkg/parse/core"
)

type CivilTask struct {
	ID    string     `json:"id" db:"id" parse:"filter,sort"`
	DueOn *core.Date `json:"due_on" db:"due_on" parse:"filter,patch,sort"`
}

type LedgerLine struct {
	ID     string           `json:"id"     db:"id"     parse:"filter,sort"`
	Amount decimal.Decimal  `json:"amount" db:"amount" parse:"filter,patch,sort"`
	Fee    *decimal.Decimal `json:"fee"    db:"fee"    parse:"filter,patch,sort"`
}

// NetworkEvent has a column of each value type with one canonical text.
type NetworkEvent struct {
	ID         string          `json:"id" db:"id" parse:"filter,sort"`
	At         *core.LocalTime `json:"at" db:"at" parse:"filter,patch,sort,facet"`
	Retention  *core.Interval  `json:"retention" db:"retention" parse:"filter,patch"`
	ActorIP    *core.Inet      `json:"actor_ip" db:"actor_ip" parse:"filter,patch"`
	Network    *core.CIDR      `json:"network" db:"network" parse:"filter,patch"`
	Device     *core.MACAddr   `json:"device" db:"device" parse:"filter,patch,facet"`
	AllowedIPs []core.Inet     `json:"allowed_ips" db:"allowed_ips" parse:"filter,patch"`
	Spans      []core.Interval `json:"spans" db:"spans" parse:"filter,patch"`
}
