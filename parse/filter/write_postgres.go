package filter

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// ChangedCondition is column IS DISTINCT FROM expr and, where a type's
// equality is coarser than the text a read returns, their texts compared
// under "C" collation: a document by its canonical jsonb text, a decimal
// by value alone.
func (PostgresDialect) ChangedCondition(column, expr string, t core.TypeInfo) string {
	switch {
	case t.SQLType == core.SQLJSON:
		// json has no equality operator, and jsonb's text is canonical:
		// key order and whitespace do not count, a number's written form
		// does. The cast to json types the placeholder as the column.
		return column + `::jsonb::text COLLATE "C" IS DISTINCT FROM (` + expr + ")::json::jsonb::text"
	case t.Type == core.FieldTypeDecimal:
		// A decimal's value has no scale (core.DecimalText binds 1.50 as
		// 1.5), and a numeric(p,s) column pads every value to s: its text
		// would differ from the bound value's on every write.
		return column + " IS DISTINCT FROM " + expr
	}
	// A type's equality can be coarser than the text a read returns — an
	// interval's units, a float's zero sign, citext, a nondeterministic
	// collation, a jsonb number's form — so the text decides. The equality
	// goes first because PostgreSQL types a placeholder at its first use and
	// the WHERE precedes the SET clause: it types the placeholder as the
	// column (an enum, a domain), where a first use as text would not.
	return column + " IS DISTINCT FROM " + expr + " OR " + column + `::text COLLATE "C" IS DISTINCT FROM (` + expr + ")::text"
}

// MergePatch compiles the merge level by level over jsonb (pgMerge):
// PostgreSQL has no RFC 7396 function. A json column is read as jsonb,
// since its cast to jsonb applies on assignment but not to a function's
// argument, and the jsonb result assigns back to it.
func (PostgresDialect) MergePatch(doc string, storage core.SQLType, patch map[string]any, bind func(any) string) (string, error) {
	if storage == core.SQLJSON {
		doc += "::jsonb"
	}
	levels := 0
	return pgMerge(doc, patch, bind, &levels)
}

// pgMerge compiles the merge of patch into the document target: the target
// when it is an object (else {}), minus the deleted keys, joined with one
// bound jsonb object holding the scalar and array members and one
// two-argument jsonb_build_object per nested object. A level with nested
// objects names its target once in a scalar subquery, and each nested level
// reads its own target from that name, so target is evaluated once, the SQL
// and its arguments grow linearly with the patch, and no call nears
// PostgreSQL's 100-argument limit however wide a level is.
func pgMerge(target string, patch map[string]any, bind func(any) string, levels *int) (string, error) {
	self := "COALESCE(jsonb_path_query_first(" + target + `, 'strict $ ? (@.type() == "object")'), '{}'::jsonb)`
	alias := ""
	if hasNestedObject(patch) {
		alias = fmt.Sprintf("_merge%d", *levels)
		*levels++
		self, target = alias+".doc", self
	}
	var deleted []string
	members := map[string]any{}
	var nested []string
	for _, k := range slices.Sorted(maps.Keys(patch)) {
		switch v := patch[k].(type) {
		case nil:
			deleted = append(deleted, k)
		case map[string]any:
			key := bind(k) + "::text"
			sub, err := pgMerge("("+self+" -> "+key+")", v, bind, levels)
			if err != nil {
				return "", err
			}
			nested = append(nested, "jsonb_build_object("+key+", "+sub+")")
		default:
			members[k] = v
		}
	}
	var expr strings.Builder
	expr.WriteString(self)
	if len(deleted) > 0 {
		expr.WriteString(" - ")
		expr.WriteString(bind(deleted))
		expr.WriteString("::text[]")
	}
	if len(members) > 0 {
		doc, err := json.Marshal(members)
		if err != nil {
			return "", fmt.Errorf("merge members: %w", err)
		}
		expr.WriteString(" || ")
		expr.WriteString(bind(string(doc)))
		expr.WriteString("::jsonb")
	}
	for _, n := range nested {
		expr.WriteString(" || ")
		expr.WriteString(n)
	}
	if alias == "" {
		return "(" + expr.String() + ")", nil
	}
	return "(SELECT " + expr.String() + " FROM (SELECT " + target + " AS doc) AS " + alias + ")", nil
}

func hasNestedObject(patch map[string]any) bool {
	for _, v := range patch {
		if _, ok := v.(map[string]any); ok {
			return true
		}
	}
	return false
}

// ArrayAppend is array_append over the array, a NULL one as the untyped
// '{}', which takes the column's array type, so BIGINT[] and UUID[] columns
// append as well as TEXT[]. It keeps element order; the UPDATE's row lock
// serialises concurrent appenders without losing values.
func (PostgresDialect) ArrayAppend(array string, element any, bind func(any) string) (string, error) {
	return "array_append(COALESCE(" + array + ", '{}'), " + bind(element) + ")", nil
}

// MemberDocument reads expr as jsonb, whatever JSON type its column
// declares: jsonb_set, #- and #> take jsonb.
func (PostgresDialect) MemberDocument(expr string) string { return expr + "::jsonb" }

// SetMember is jsonb_set, which creates a missing last member: the
// member's path bound as text[], its value as jsonb.
func (PostgresDialect) SetMember(doc string, member []string, value json.RawMessage, bind func(any) string) string {
	path := pgMemberPath(member, bind)
	return "jsonb_set(" + doc + ", " + path + ", " + bind(string(value)) + "::jsonb, true)"
}

// RemoveMember is #-, the member's path bound as text[].
func (PostgresDialect) RemoveMember(doc string, member []string, bind func(any) string) string {
	return "(" + doc + " #- " + pgMemberPath(member, bind) + ")"
}

// HasMember reads the member with #>, NULL where it is missing.
func (PostgresDialect) HasMember(doc string, member []string, bind func(any) string) string {
	return "((" + doc + " #> " + pgMemberPath(member, bind) + ") IS NOT NULL)"
}

// IsObject is jsonb_typeof of the member, read with #>.
func (PostgresDialect) IsObject(doc string, member []string, bind func(any) string) string {
	if len(member) > 0 {
		doc = "(" + doc + " #> " + pgMemberPath(member, bind) + ")"
	}
	return "COALESCE(jsonb_typeof(" + doc + ") = 'object', FALSE)"
}

// pgMemberPath binds member as the text[] path #>, #- and jsonb_set take.
func pgMemberPath(member []string, bind func(any) string) string {
	return bind(slices.Clone(member)) + "::text[]"
}

// RelationRows binds the owners and the values each as a JSON array of
// records read as rel.JoinTable's own row type (jsonb_populate_recordset),
// which types each key as its column: PostgreSQL cannot type an untyped
// array outside a comparison, and a key's Go type does not name its
// column's type (a string key of a uuid column). A record's member is the
// column's exact name, the name the quoted column reads. The tenant is a
// member of each owner record.
func (d PostgresDialect) RelationRows(rel *core.RelationSchema, owners, values []core.Key, tenant *core.PairTenant, bind func(any) string) (core.PairRows, error) {
	var tenantValue any
	if tenant != nil {
		v, err := tenant.Record()
		if err != nil {
			return core.PairRows{}, fmt.Errorf("tenant: %w", err)
		}
		tenantValue = v
	}
	ownerRows := make([]map[string]any, len(owners))
	for i, k := range owners {
		ownerRows[i] = make(map[string]any, len(k)+1)
		for j, c := range rel.SourceColumns {
			ownerRows[i][c] = k[j]
		}
		if tenant != nil {
			ownerRows[i][rel.TenantColumn] = tenantValue
		}
	}
	valueRows := make([]map[string]any, len(values))
	for i, k := range values {
		valueRows[i] = make(map[string]any, len(k))
		for j, c := range rel.TargetColumns {
			valueRows[i][c] = k[j]
		}
	}
	ownersJSON, err := json.Marshal(ownerRows)
	if err != nil {
		return core.PairRows{}, err
	}
	valuesJSON, err := json.Marshal(valueRows)
	if err != nil {
		return core.PairRows{}, err
	}
	rows := core.PairRows{Sources: make([]string, len(rel.SourceColumns)), Targets: make([]string, len(rel.TargetColumns))}
	for i, c := range rel.SourceColumns {
		rows.Sources[i] = qualified(d, "o", c)
	}
	for i, c := range rel.TargetColumns {
		rows.Targets[i] = qualified(d, "v", c)
	}
	if tenant != nil {
		rows.Tenant = qualified(d, "o", rel.TenantColumn)
	}
	row := "NULL::" + d.QuoteIdent(rel.JoinTable)
	rows.From = " FROM jsonb_populate_recordset(" + row + ", " + bind(string(ownersJSON)) + "::jsonb) AS o, jsonb_populate_recordset(" + row + ", " + bind(string(valuesJSON)) + "::jsonb) AS v"
	return rows, nil
}
