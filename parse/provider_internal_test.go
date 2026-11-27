//go:build unit

package parse

import (
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/backend"
)

// compiling is a provider for d with no database, for an internal test of
// what a resource builds; internal/providertest.Compile is its counterpart
// for the package's external tests, which can import a provider package.
func compiling(d filter.Dialect) *Provider {
	return NewProvider(backend.Provider{Name: d.Name(), Dialect: d, Capabilities: backend.AllCapabilities})
}
