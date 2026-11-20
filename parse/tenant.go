package parse

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/ashtonian/pkg/parse/core"
)

// ErrTenantInvalid is a Scope.Tenant a tenant column the request reaches
// cannot hold: text that is not a UUID for a UUID column, or not a decimal
// integer in range for an integer column. Like ErrTenantRequired, it is the
// server's mistake, never the request's.
var ErrTenantInvalid = errors.New("parse: Scope.Tenant is not a value of the tenant column's type")

// tenantColumn is a resource's TenantColumn and its Go type.
type tenantColumn struct {
	table, column string
	info          core.TypeInfo
}

func newTenantColumn(table, column string, t reflect.Type) (*tenantColumn, error) {
	info := core.TypeOf(t)
	switch info.Type {
	case core.FieldTypeString, core.FieldTypeUUID, core.FieldTypeInt:
		return &tenantColumn{table: table, column: column, info: info}, nil
	}
	return nil, fmt.Errorf("tenant column %s is %s; a tenant key is a string, a UUID or an integer", column, info.Type)
}

// name is the column as an error about table names it: bare for table's
// own, which the error already names by table.
func (c *tenantColumn) name(table string) string {
	if c.table == table {
		return c.column
	}
	return c.table + "." + c.column
}

// value is text as c holds it: the text of a string, the canonical text of
// a UUID, an int64 or uint64 in the range of an integer field's Go kind.
func (c *tenantColumn) value(text string) (any, error) {
	if c.info.Type != core.FieldTypeInt {
		return core.NormalizeScalar(c.info.Type, c.info.NumberKind, text)
	}
	// Decimal digits with an optional minus sign: the text an integer key
	// is written as, and no other spelling of it.
	var (
		n   any
		err error
	)
	if strings.HasPrefix(text, "-") {
		n, err = strconv.ParseInt(text, 10, 64)
	} else {
		n, err = strconv.ParseUint(text, 10, 64)
	}
	switch {
	case errors.Is(err, strconv.ErrRange):
		return nil, fmt.Errorf("%s overflows %s", text, c.info.NumberKind)
	case err != nil:
		return nil, fmt.Errorf("%q is not a decimal integer", text)
	}
	return core.NormalizeScalar(c.info.Type, c.info.NumberKind, n)
}

// textual reports a column holding the tenant as text: a string, or a UUID
// whose canonical text a string column holds as well.
func (c *tenantColumn) textual() bool { return c.info.Type != core.FieldTypeInt }

// bindsForAll reports a column whose value every column of its family
// holds: a UUID's canonical text, which a string column holds too, and a
// signed integer, which every integer column holds when it is in range.
func (c *tenantColumn) bindsForAll() bool {
	if c.info.Type == core.FieldTypeUUID {
		return true
	}
	return c.info.Type == core.FieldTypeInt && c.info.NumberKind >= reflect.Int && c.info.NumberKind <= reflect.Int64
}

// tenantKey is the one value a request's tenant binds as at every tenant
// column the resource's statements reach: one column of each distinct type
// among them, in the order they are reached, each checking the tenant, and
// the value of columns[binds]. A tenant one cannot hold is refused before
// any statement is built. With no column, the text binds as given.
type tenantKey struct {
	columns []*tenantColumn
	binds   int
}

// tenantConflict is a textual and an integer tenant column one resource
// reaches: no one value binds to both.
type tenantConflict struct{ first, other *tenantColumn }

// tenantKeyOf is the key of the reached columns, or the first two of them
// no one value binds to.
func tenantKeyOf(reached []*tenantColumn) (tenantKey, *tenantConflict) {
	var key tenantKey
	for _, c := range reached {
		if c.textual() != reached[0].textual() {
			return tenantKey{}, &tenantConflict{first: reached[0], other: c}
		}
		if slices.ContainsFunc(key.columns, func(k *tenantColumn) bool {
			return k.info.Type == c.info.Type && k.info.NumberKind == c.info.NumberKind
		}) {
			continue
		}
		key.columns = append(key.columns, c)
	}
	if i := slices.IndexFunc(key.columns, (*tenantColumn).bindsForAll); i >= 0 {
		key.binds = i
	}
	return key, nil
}

// value is text, a scope's tenant, as every column of the key holds it. The
// error names the column, relative to table, that cannot hold it.
func (k tenantKey) value(table, text string) (any, error) {
	if len(k.columns) == 0 {
		return text, nil
	}
	var bound any
	for i, c := range k.columns {
		v, err := c.value(text)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrTenantInvalid, c.name(table), err)
		}
		if i == k.binds {
			bound = v
		}
	}
	return bound, nil
}

// tenantNode is a resource in the graph its relations make. A relation
// predicate nests the target's own filter, relations included, and binds
// the request's one tenant at every depth; includes, facets and membership
// writes bind it to a relation's target. So a resource's tenant key is read
// from every tenant column it reaches, transitively, and changes when any
// resource it reaches gains a relation.
type tenantNode struct {
	table string
	own   *tenantColumn
	edges []tenantEdge
	// owners are the resources with a relation to this one, whose keys
	// depend on it.
	owners []*tenantNode
	key    tenantKey
}

type tenantEdge struct {
	to *tenantNode
	// policy marks a policy-scoped target: its policy, not the tenant,
	// holds its rows, but its relations bind the tenant as any target's do.
	policy bool
}

func newTenantNode(table string, own *tenantColumn) *tenantNode {
	n := &tenantNode{table: table, own: own}
	if own != nil {
		n.key = tenantKey{columns: []*tenantColumn{own}}
	}
	return n
}

// relate adds n's relation to to, and with it to's tenant columns to the
// key of n and of every resource that reaches n. When one of those keys
// would hold a textual and an integer column, the relation is refused and
// nothing changes.
func (n *tenantNode) relate(to *tenantNode, policy bool) error {
	n.edges = append(n.edges, tenantEdge{to: to, policy: policy})
	dependants := n.dependants()
	keys := make([]tenantKey, len(dependants))
	for i, d := range dependants {
		key, conflict := tenantKeyOf(d.reached())
		if conflict != nil {
			n.edges = n.edges[:len(n.edges)-1]
			a, b := conflict.first, conflict.other
			if d == n {
				return fmt.Errorf("tenant columns %s (%s) and %s (%s) hold different types, but one request binds its tenant to both",
					a.name(n.table), a.info.Type, b.name(n.table), b.info.Type)
			}
			return fmt.Errorf("%s binds one request's tenant to tenant columns %s (%s) and %s (%s), which hold different types",
				d.table, a.name(n.table), a.info.Type, b.name(n.table), b.info.Type)
		}
		keys[i] = key
	}
	to.owners = append(to.owners, n)
	for i, d := range dependants {
		d.key = keys[i]
	}
	return nil
}

// dependants is n and every resource whose relations reach it.
func (n *tenantNode) dependants() []*tenantNode {
	out := []*tenantNode{n}
	seen := map[*tenantNode]bool{n: true}
	for i := 0; i < len(out); i++ {
		for _, o := range out[i].owners {
			if !seen[o] {
				seen[o] = true
				out = append(out, o)
			}
		}
	}
	return out
}

// reached is every tenant column n's statements bind the tenant to, its own
// first: through each relation, the target's own unless policy-scoped, and
// the columns the target's relations reach.
func (n *tenantNode) reached() []*tenantColumn {
	var cols []*tenantColumn
	if n.own != nil {
		cols = append(cols, n.own)
	}
	bound := map[*tenantNode]bool{n: true}
	queue := []*tenantNode{n}
	queued := map[*tenantNode]bool{n: true}
	for i := 0; i < len(queue); i++ {
		for _, e := range queue[i].edges {
			if !e.policy && !bound[e.to] {
				bound[e.to] = true
				if e.to.own != nil {
					cols = append(cols, e.to.own)
				}
			}
			if !queued[e.to] {
				queued[e.to] = true
				queue = append(queue, e.to)
			}
		}
	}
	return cols
}

// bindTenant is text, a scope's tenant, as every statement of the request
// binds it: checked and normalized once, here, against every tenant column
// the resource reaches. Nil when the scope has none; ErrTenantRequired when
// the resource has a tenant column.
func (r *Resource[T]) bindTenant(text string) (any, error) {
	if text == "" {
		if r.tenant != "" {
			return nil, fmt.Errorf("%s: %w", r.table, ErrTenantRequired)
		}
		return nil, nil
	}
	v, err := r.tenantNode.key.value(r.table, text)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.table, err)
	}
	return v, nil
}

func (r *Resource[T]) tenantGraph() *tenantNode { return r.tenantNode }
