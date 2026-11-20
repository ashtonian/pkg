package parse

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"github.com/ashtonian/pkg/parse/core"
)

// ErrTenantInvalid is a Scope.Tenant the tenant columns cannot hold: text
// that is not a decimal integer in range for an integer column, or not a UUID
// for a UUID column. Like ErrTenantRequired, it is the server's mistake,
// never the request's.
var ErrTenantInvalid = errors.New("parse: Scope.Tenant is not a value of the tenant column's type")

// tenantKey is the type of the tenant every statement of a resource binds:
// its own tenant column's, or, for a resource without one, its relation
// targets'. One request has one tenant, so every tenant column a
// resource's statements reach holds the same type.
type tenantKey struct {
	// column is the table.column the type was taken from, for errors.
	column string
	info   core.TypeInfo
}

func newTenantKey(table, column string, t reflect.Type) (*tenantKey, error) {
	info := core.TypeOf(t)
	switch info.Type {
	case core.FieldTypeString, core.FieldTypeUUID, core.FieldTypeInt:
		return &tenantKey{column: table + "." + column, info: info}, nil
	}
	return nil, fmt.Errorf("tenant column %s.%s is %s; a tenant key is a string, a UUID or an integer", table, column, info.Type)
}

// value is text, a scope's tenant, as the key's type holds it: the text of a
// string, the canonical text of a UUID, an int64 or uint64 for an integer.
// An integer is decimal digits with an optional sign, in the range of the
// field's Go kind.
func (k *tenantKey) value(text string) (any, error) {
	var (
		v   any = text
		err error
	)
	if k.info.Type == core.FieldTypeInt {
		v, err = parseTenantInt(text, k.info.NumberKind)
	}
	if err == nil {
		v, err = core.NormalizeScalar(k.info.Type, k.info.NumberKind, v)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %q for %s: %w", ErrTenantInvalid, text, k.column, err)
	}
	return v, nil
}

func parseTenantInt(text string, kind reflect.Kind) (any, error) {
	switch kind {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.ParseUint(text, 10, 64)
	}
	return strconv.ParseInt(text, 10, 64)
}

// agrees reports whether another tenant column holds the same type, so one
// normalized tenant binds to both.
func (k *tenantKey) agrees(other *tenantKey) bool { return k.info.Type == other.info.Type }

// scopeTenant is text, a scope's tenant, in the resource's tenant key type;
// nil when the scope has none. A resource whose statements reach no typed
// tenant column — at most a pair table's, which has no Go type — binds the
// text as it is.
func (r *Resource[T]) scopeTenant(text string) (any, error) {
	if text == "" {
		return nil, nil
	}
	if r.tenantKey == nil {
		return text, nil
	}
	v, err := r.tenantKey.value(text)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", r.table, err)
	}
	return v, nil
}

// ownTenantKey is the key type of the resource's own tenant column, nil
// without one.
func (r *Resource[T]) ownTenantKey() *tenantKey {
	if r.tenant == "" {
		return nil
	}
	return r.tenantKey
}
