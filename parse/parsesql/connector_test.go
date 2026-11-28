//go:build unit || integration || concurrency

package parsesql_test

import (
	"context"
	"database/sql/driver"
)

// connector opens dsn through driver, which it need not be registered
// under a name for.
type connector struct {
	driver driver.Driver
	dsn    string
}

func (c connector) Connect(context.Context) (driver.Conn, error) { return c.driver.Open(c.dsn) }

func (c connector) Driver() driver.Driver { return c.driver }
