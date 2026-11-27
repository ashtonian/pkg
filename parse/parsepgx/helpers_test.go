//go:build unit || integration

package parsepgx_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/internal/providertest"
	"github.com/ashtonian/pkg/parse/parsepgx"
)

// configOn is the default Config on dsn.
func configOn(t *testing.T, dsn string) parsepgx.Config {
	t.Helper()
	cfg := providertest.Defaults[parsepgx.Config](t)
	cfg.DSN = dsn
	return cfg
}
