package parsememory

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/backend"
)

// ErrTableMismatch is a resource whose table disagrees with what another
// resource on the same table declared: its key, or the type of a column
// both declare. The resource is refused and declares nothing.
var ErrTableMismatch = errors.New("parsememory: resources disagree on a table")

// column is one column of a table and the type its values hold.
type column struct {
	name string
	typ  core.TypeInfo
}

// tableDef is a table's shape: its columns, the key among them, and
// whether it is a relation's pair table. A published definition never
// changes; a declaration that adds a column publishes a new one, and a row
// written before it holds the column as NULL. The key is fixed by the
// first declaration, so a table's rows keep their order.
type tableDef struct {
	name    string
	columns []column
	index   map[string]int
	// key are the key columns' positions in columns, in key order.
	key []int
	// pair reports a pair table no resource declares, whose key is every
	// column its relations declare.
	pair bool
}

func newTableDef(name string, columns []column, key []string, pair bool) (*tableDef, error) {
	def := &tableDef{name: name, index: make(map[string]int, len(columns)), pair: pair}
	for _, c := range columns {
		at, ok := def.index[c.name]
		if ok {
			if !typesAgree(def.columns[at].typ, c.typ) {
				return nil, fmt.Errorf("%w: table %q: column %q is declared as %s and as %s", ErrTableMismatch, name, c.name, typeText(def.columns[at].typ), typeText(c.typ))
			}
			continue
		}
		def.index[c.name] = len(def.columns)
		def.columns = append(def.columns, c)
	}
	if len(key) == 0 {
		return nil, fmt.Errorf("parsememory: table %q: no key column", name)
	}
	for _, k := range key {
		at, ok := def.index[k]
		if !ok {
			return nil, fmt.Errorf("parsememory: table %q: key column %q is no column of the table", name, k)
		}
		def.key = append(def.key, at)
	}
	return def, nil
}

// keyNames are the key columns, in key order.
func (d *tableDef) keyNames() []string {
	names := make([]string, len(d.key))
	for i, at := range d.key {
		names[i] = d.columns[at].name
	}
	return names
}

// keyTypes are the key columns' types, in key order.
func (d *tableDef) keyTypes() []core.TypeInfo {
	types := make([]core.TypeInfo, len(d.key))
	for i, at := range d.key {
		types[i] = d.columns[at].typ
	}
	return types
}

// typesAgree reports whether two declarations of one column store its
// values alike: the same type and element type, both or neither declared
// JSON (a Valuer's column stores its value unless it is), and a float's
// width, which the values it holds round to; or one leaves the column
// untyped (a pair table's tenant column whose owner and target hold no
// tenant). An integer's Go kind does not change what is stored, nor does a
// storage declaration the type already implies (core.SQLDate on a date).
func typesAgree(a, b core.TypeInfo) bool {
	if a == (core.TypeInfo{}) || b == (core.TypeInfo{}) {
		return true
	}
	return a.Type == b.Type && a.Elem == b.Elem && a.SQLType.IsJSON() == b.SQLType.IsJSON() && float32Column(a) == float32Column(b)
}

// float32Column reports a column, or an array's elements, of float32
// values.
func float32Column(t core.TypeInfo) bool {
	return t.NumberKind == reflect.Float32
}

func typeText(t core.TypeInfo) string {
	if t == (core.TypeInfo{}) {
		return "untyped"
	}
	text := string(t.Type)
	if t.Elem != "" {
		text += " of " + string(t.Elem)
	}
	if t.SQLType != "" {
		text += " (" + string(t.SQLType) + ")"
	}
	if t.NumberKind != 0 && t.Type != core.FieldTypeArray {
		text += " (" + t.NumberKind.String() + ")"
	}
	return text
}

// catalog holds every table's definition. Declarations are serialized and
// publish a new set; reads load the published set without a lock. It is
// apart from the rows (store), so a declaration never waits for a writer.
type catalog struct {
	mu     sync.Mutex
	tables atomic.Pointer[map[string]*tableDef]
}

func newCatalog() *catalog {
	c := &catalog{}
	empty := map[string]*tableDef{}
	c.tables.Store(&empty)
	return c
}

// table is the named table's definition.
func (c *catalog) table(name string) (*tableDef, bool) {
	def, ok := (*c.tables.Load())[name]
	return def, ok
}

// declare declares res's table and the pair tables of its many-to-many
// relations (backend.Declarer), all or none of them.
func (c *catalog) declare(res *backend.Resource) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := maps.Clone(*c.tables.Load())
	decl, err := resourceTable(res)
	if err != nil {
		return err
	}
	merged, err := mergeTable(next[res.Table], decl)
	if err != nil {
		return err
	}
	next[res.Table] = merged
	if res.Schema != nil {
		for _, name := range slices.Sorted(maps.Keys(res.Schema.Fields)) {
			rel := res.Schema.Fields[name].Relation
			if rel == nil || rel.Kind != core.ManyToMany {
				continue
			}
			decl, err := pairTable(rel, res, next)
			if err != nil {
				return fmt.Errorf("parsememory: relation %q: %w", name, err)
			}
			merged, err := mergeTable(next[rel.JoinTable], decl)
			if err != nil {
				return fmt.Errorf("parsememory: relation %q: %w", name, err)
			}
			next[rel.JoinTable] = merged
		}
	}
	c.tables.Store(&next)
	return nil
}

// resourceTable is the table res reads: every column of its type, keyed by
// its key.
func resourceTable(res *backend.Resource) (*tableDef, error) {
	columns := make([]column, len(res.TableColumns))
	for i, c := range res.TableColumns {
		columns[i] = column{name: c.Name, typ: c.Type}
	}
	return newTableDef(res.Table, columns, res.Key, false)
}

// pairTable is the pair table of rel, a many-to-many of res: its source,
// target and tenant columns, keyed by all of them. The tenant column holds
// the owner's tenant, or else the target's, and is typed as that column;
// untyped when neither holds one.
func pairTable(rel *core.RelationSchema, res *backend.Resource, tables map[string]*tableDef) (*tableDef, error) {
	err := rel.Check()
	if err != nil {
		return nil, err
	}
	var columns []column
	for i, c := range rel.SourceColumns {
		columns = append(columns, column{name: c, typ: rel.SourceTypes[i]})
	}
	for i, c := range rel.TargetColumns {
		columns = append(columns, column{name: c, typ: rel.TargetTypes[i]})
	}
	if rel.TenantColumn != "" {
		columns = append(columns, column{name: rel.TenantColumn, typ: pairTenantType(rel, res, tables)})
	}
	key := make([]string, len(columns))
	for i, c := range columns {
		key[i] = c.name
	}
	return newTableDef(rel.JoinTable, columns, slices.Compact(key), true)
}

// pairTenantType is the type of rel's pair tenant column: the owner's
// tenant column's, else the target's, else untyped.
func pairTenantType(rel *core.RelationSchema, res *backend.Resource, tables map[string]*tableDef) core.TypeInfo {
	if res.Tenant != "" {
		for _, c := range res.TableColumns {
			if c.Name == res.Tenant {
				return c.Type
			}
		}
	}
	if rel.TargetTable != "" && rel.TargetTenant != "" {
		target, ok := tables[rel.TargetTable]
		if ok {
			at, ok := target.index[rel.TargetTenant]
			if ok {
				return target.columns[at].typ
			}
		}
	}
	return core.TypeInfo{}
}

// mergeTable is decl declared over existing, the table as declared before
// (nil for none). Resources on one table agree on its key; a pair table's
// relations agree on its columns; a resource on a pair table keys it by
// the pair's columns, and a relation through a resource's table finds its
// columns there. Every column both declare agrees on its type.
func mergeTable(existing, decl *tableDef) (*tableDef, error) {
	if existing == nil {
		return decl, nil
	}
	mismatch := func(format string, args ...any) error {
		return fmt.Errorf("%w: table %q: %s", ErrTableMismatch, decl.name, fmt.Sprintf(format, args...))
	}
	for _, c := range decl.columns {
		at, ok := existing.index[c.name]
		if ok && !typesAgree(existing.columns[at].typ, c.typ) {
			return nil, mismatch("column %q is %s, declared before as %s", c.name, typeText(c.typ), typeText(existing.columns[at].typ))
		}
	}
	switch {
	case existing.pair && decl.pair:
		if !sameColumns(existing, decl) {
			return nil, mismatch("its relations pair (%s) and (%s); a pair table's rows are keyed by every column, so its relations declare the same", columnList(existing), columnList(decl))
		}
		return existing, nil
	case !existing.pair && decl.pair:
		return withColumns(existing, decl, existing.key, false), nil
	case existing.pair:
		if !slices.Equal(existing.keyNames(), decl.keyNames()) {
			return nil, mismatch("a pair table keyed by (%s) is declared with the key (%s)", strings.Join(existing.keyNames(), ", "), strings.Join(decl.keyNames(), ", "))
		}
		return withColumns(existing, decl, existing.key, false), nil
	}
	if !slices.Equal(existing.keyNames(), decl.keyNames()) {
		return nil, mismatch("its key is (%s), declared before as (%s)", strings.Join(decl.keyNames(), ", "), strings.Join(existing.keyNames(), ", "))
	}
	return withColumns(existing, decl, existing.key, false), nil
}

// withColumns is existing with decl's columns it lacks appended, after its
// own, so every row written before keeps its columns' positions.
func withColumns(existing, decl *tableDef, key []int, pair bool) *tableDef {
	merged := &tableDef{name: existing.name, columns: slices.Clone(existing.columns), index: maps.Clone(existing.index), key: slices.Clone(key), pair: pair}
	for _, c := range decl.columns {
		_, ok := merged.index[c.name]
		if ok {
			continue
		}
		merged.index[c.name] = len(merged.columns)
		merged.columns = append(merged.columns, c)
	}
	return merged
}

// sameColumns reports whether two definitions name the same columns.
func sameColumns(a, b *tableDef) bool {
	if len(a.columns) != len(b.columns) {
		return false
	}
	for _, c := range a.columns {
		_, ok := b.index[c.name]
		if !ok {
			return false
		}
	}
	return true
}

func columnList(d *tableDef) string {
	names := make([]string, len(d.columns))
	for i, c := range d.columns {
		names[i] = c.name
	}
	return strings.Join(names, ", ")
}
