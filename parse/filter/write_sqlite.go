package filter

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/dialect"
)

// ChangedCondition is column COLLATE BINARY IS NOT expr. IS NOT is SQLite's
// null-safe inequality on every version that runs the SQL (IS DISTINCT
// FROM is a 3.39 alias). BINARY overrides a NOCASE or RTRIM column and
// keeps its affinity, which converts the value for the comparison as it
// does on store.
func (SQLiteDialect) ChangedCondition(column, expr string, _ core.TypeInfo) string {
	return column + " COLLATE BINARY IS NOT " + expr
}

// ChangedDocumentCondition compares the document's JSON text as
// ChangedCondition compares any column: json_set and json_remove return
// the text the column then holds.
func (d SQLiteDialect) ChangedDocumentCondition(column, doc string) string {
	return d.ChangedCondition(column, doc, core.TypeInfo{})
}

// MergePatch is json_patch, SQLite's RFC 7396, with the patch bound as its
// JSON text and a NULL document merged as {}.
func (SQLiteDialect) MergePatch(doc string, _ core.SQLType, patch map[string]any, bind func(any) string) (string, error) {
	text, err := json.Marshal(patch)
	if err != nil {
		return "", err
	}
	return "json_patch(COALESCE(" + doc + ", '{}'), " + bind(string(text)) + ")", nil
}

// ArrayAppend inserts at '$[#]' of the JSON text array, a NULL one as []. The
// element is bound as its JSON encoding — the one a full replace writes —
// and read back through json(): a bound SQL value would be inserted as a
// SQL scalar, so a Go true would land as the integer 1.
func (SQLiteDialect) ArrayAppend(array string, element any, bind func(any) string) (string, error) {
	data, err := json.Marshal(element)
	if err != nil {
		return "", fmt.Errorf("encoding the appended element: %w", err)
	}
	return "json_insert(COALESCE(" + array + ", '[]'), '$[#]', json(" + bind(string(data)) + "))", nil
}

// MemberDocument is expr itself: SQLite's JSON functions read its text.
func (SQLiteDialect) MemberDocument(expr string) string { return expr }

// SetMember is json_set at the member's JSON path (dialect.SQLiteJSONPath),
// its value read back through json() so a document stays one.
func (SQLiteDialect) SetMember(doc string, member []string, value json.RawMessage, bind func(any) string) string {
	path := bind(dialect.SQLiteJSONPath(member))
	return "json_set(" + doc + ", " + path + ", json(" + bind(string(value)) + "))"
}

// RemoveMember is json_remove at the member's JSON path.
func (SQLiteDialect) RemoveMember(doc string, member []string, bind func(any) string) string {
	return "json_remove(" + doc + ", " + bind(dialect.SQLiteJSONPath(member)) + ")"
}

// HasMember reads the member's json_type, NULL where it is missing.
func (SQLiteDialect) HasMember(doc string, member []string, bind func(any) string) string {
	return "(json_type(" + doc + ", " + bind(dialect.SQLiteJSONPath(member)) + ") IS NOT NULL)"
}

// IsObject compares the member's json_type with 'object'.
func (SQLiteDialect) IsObject(doc string, member []string, bind func(any) string) string {
	typ := "json_type(" + doc + ")"
	if len(member) > 0 {
		typ = "json_type(" + doc + ", " + bind(dialect.SQLiteJSONPath(member)) + ")"
	}
	return "COALESCE(" + typ + " = 'object', FALSE)"
}

// RelationRows reads the owners and the values each from one bound JSON
// array with json_each: a one-column key as each row's value, a longer
// key's columns by position from each row's array. SQLite types values by
// the column's affinity on insert, so no row type is needed. The tenant
// binds as a parameter of its own, and the condition true makes a WHERE
// that parts the upsert's ON CONFLICT from a join's ON.
func (d SQLiteDialect) RelationRows(rel *core.RelationSchema, owners, values []core.Key, tenant *core.PairTenant, bind func(any) string) (core.PairRows, error) {
	bindKeys := func(keys []core.Key) (string, error) {
		var doc any = keys
		if len(keys) > 0 && len(keys[0]) == 1 {
			scalars := make([]any, len(keys))
			for i, k := range keys {
				scalars[i] = k[0]
			}
			doc = scalars
		}
		arr, err := d.MarshalValue(doc, core.TypeInfo{Type: core.FieldTypeArray})
		if err != nil {
			return "", err
		}
		return bind(arr), nil
	}
	ownersParam, err := bindKeys(owners)
	if err != nil {
		return core.PairRows{}, err
	}
	valuesParam, err := bindKeys(values)
	if err != nil {
		return core.PairRows{}, err
	}
	read := func(alias string, width int) []string {
		if width == 1 {
			columns := []string{alias + ".value"}
			return columns
		}
		positions := make([]string, width)
		for i := range positions {
			positions[i] = "json_extract(" + alias + ".value, '$[" + strconv.Itoa(i) + "]')"
		}
		return positions
	}
	rows := core.PairRows{
		Sources: read("o", len(rel.SourceColumns)),
		Targets: read("v", len(rel.TargetColumns)),
		From:    " FROM json_each(" + ownersParam + ") AS o, json_each(" + valuesParam + ") AS v",
		Where:   []string{"true"},
	}
	if tenant != nil {
		rows.Tenant = bind(tenant.Value)
	}
	return rows, nil
}
