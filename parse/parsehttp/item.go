package parsehttp

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
)

// Keyed is a resource whose rows a key of one or more columns names, read
// from the columns' texts; *parse.Resource[T] is one.
type Keyed interface {
	PrimaryKeyColumns() []string
	ParseKey(texts ...string) (parse.Key, error)
}

// ErrNoKeyValue is an item request whose route holds no value for a key
// column: a route registered without ItemPattern's wildcards, or a router
// that matched an empty segment. Behind a ServeMux route registered with
// ItemPattern it never happens, so there it is a server defect.
var ErrNoKeyValue = errors.New("no path value")

// ItemPattern is the ServeMux pattern of one row below its collection's
// path: a wildcard per key column, named for the column, in key order. A
// one-column key is one segment ("{id}"), a key of several a segment per
// column ("{warehouse_id}/{sku}"), so an item URL escapes each key value on
// its own and a value may hold "/" (as %2F). Register the item route as the
// collection's path, a slash and ItemPattern, and read the key with
// ItemKey. A key column whose name is no wildcard name, as a column
// mapper's may be, is an error.
func ItemPattern(res Keyed) (string, error) {
	columns := res.PrimaryKeyColumns()
	if len(columns) == 0 {
		return "", errors.New("parsehttp: item pattern: the resource has no key column")
	}
	wildcards := make([]string, len(columns))
	for i, column := range columns {
		if !core.IsIdentifier(column) {
			return "", fmt.Errorf("parsehttp: item pattern: key column %q is no ServeMux wildcard name", column)
		}
		wildcards[i] = "{" + column + "}"
	}
	return strings.Join(wildcards, "/"), nil
}

// ItemKey reads the key of the row r's item route names: the path value of
// each of ItemPattern's wildcards, which ServeMux unescapes segment by
// segment, read by Keyed.ParseKey as its column's text. A key column without
// a value is ErrNoKeyValue, and a text its column cannot hold (letters for
// an integer column, U+0000 or invalid UTF-8) parse.ErrNoSuchKey; each
// names the column. No row has a key its columns cannot hold, so answer
// parse.ErrNoSuchKey as the row not found.
func ItemKey(r *http.Request, res Keyed) (parse.Key, error) {
	columns := res.PrimaryKeyColumns()
	texts := make([]string, len(columns))
	for i, column := range columns {
		texts[i] = r.PathValue(column)
		if texts[i] == "" {
			return nil, fmt.Errorf("parsehttp: item key column %s: %w", column, ErrNoKeyValue)
		}
	}
	return res.ParseKey(texts...)
}
