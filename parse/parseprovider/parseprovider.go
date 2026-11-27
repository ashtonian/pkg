// Package parseprovider opens the one parse provider an application's
// configuration names, among the providers its binary links. Each provider
// module owns its settings and how it opens (parsesql.Config and
// parsesql.Open, parsepgx.Config and parsepgx.Open); main hands Open a
// Choice for each provider it links, built from that provider's settings
// (parsesql.Choice, parsepgx.Choice). This package imports no provider, so a
// binary carries only the drivers of the providers it passes. The providers
// are alternatives: nothing here moves data between them or runs one beside
// another.
package parseprovider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/parse"
)

// Database is an opened provider and the database under it. Each
// provider's own type (parsesql.Database, parsepgx.Database) also hands out
// its database handle, for the application's own SQL.
type Database interface {
	// Provider is the provider to build resources on (parse.NewResource).
	Provider() *parse.Provider
	// Ping checks that the database answers, for a readiness probe.
	Ping(ctx context.Context) error
	// Close closes the database once the work on it is done.
	Close() error
}

// Choice is a provider the binary links: its name in configuration
// (parse.Provider.Name), and how it opens from the settings it was built
// with.
type Choice struct {
	Name string
	Open func(ctx context.Context) (Database, error)
}

// Open opens the choice named name under ctx. It refuses a name no choice
// has, listing the providers the binary links, and choices that repeat a
// name or have no Open.
func Open(ctx context.Context, name string, choices ...Choice) (Database, error) {
	names := make([]string, 0, len(choices))
	var chosen *Choice
	for i, c := range choices {
		switch {
		case c.Name == "":
			return nil, fmt.Errorf("parseprovider: choice %d: no name", i)
		case c.Open == nil:
			return nil, fmt.Errorf("parseprovider: choice %q: no Open", c.Name)
		case slices.Contains(names, c.Name):
			return nil, fmt.Errorf("parseprovider: choice %q: given twice", c.Name)
		}
		names = append(names, c.Name)
		if c.Name == name {
			chosen = &choices[i]
		}
	}
	if len(names) == 0 {
		return nil, errors.New("parseprovider: no provider to choose from")
	}
	if chosen == nil {
		slices.Sort(names)
		return nil, fmt.Errorf("parseprovider: provider: %q: this binary links %s", name, strings.Join(names, ", "))
	}
	db, err := chosen.Open(ctx)
	if err != nil {
		return nil, err
	}
	return db, nil
}
