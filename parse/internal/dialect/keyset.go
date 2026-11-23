package dialect

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// KeySet renders row, a parenthesized row value of table's columns in
// order, IN the keys (NOT IN when member is false), the keys bound as one
// parameter whatever their number. PostgreSQL reads them back as rows of
// table's own row type (jsonb_populate_recordset), so each position
// compares as its column's type, a string as a uuid where the column is
// one, and the key index serves the semi-join. SQLite reads each position
// of a JSON array of arrays with json_extract.
func KeySet(d Dialect, row, table string, columns []string, keys []core.Key, member bool, bind func(any) string) (string, error) {
	in := " IN "
	if !member {
		in = " NOT IN "
	}
	for i, k := range keys {
		if len(k) != len(columns) {
			return "", fmt.Errorf("key set on %s: key %d has %d values for %d columns", table, i, len(k), len(columns))
		}
	}
	switch k := d.kind(); k {
	case KindPostgres:
		doc := make([]map[string]any, len(keys))
		for i, key := range keys {
			doc[i] = make(map[string]any, len(columns))
			for j, c := range columns {
				doc[i][c] = key[j]
			}
		}
		arr, err := json.Marshal(doc)
		if err != nil {
			return "", fmt.Errorf("key set on %s: %w", table, err)
		}
		selected := make([]string, len(columns))
		for i, c := range columns {
			selected[i] = d.QuoteIdent("k") + "." + d.QuoteIdent(c)
		}
		return row + in + "(SELECT " + strings.Join(selected, ", ") + " FROM jsonb_populate_recordset(NULL::" + d.QuoteIdent(table) +
			", " + bind(string(arr)) + ") AS " + d.QuoteIdent("k") + ")", nil
	case KindSQLite:
		arr, err := json.Marshal(keys)
		if err != nil {
			return "", fmt.Errorf("key set on %s: %w", table, err)
		}
		positions := make([]string, len(columns))
		for i := range columns {
			positions[i] = "json_extract(value, '$[" + strconv.Itoa(i) + "]')"
		}
		return row + in + "(SELECT " + strings.Join(positions, ", ") + " FROM json_each(" + bind(string(arr)) + "))", nil
	default:
		return "", Unsupported(k, "a key set")
	}
}
