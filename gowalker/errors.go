package gowalker

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/ashtonian/pkg/gowalker/path"
)

var (
	// ErrSchema identifies invalid field rules, ambiguous names, or invalid engine settings.
	ErrSchema = errors.New("invalid schema")
	// ErrPath identifies an invalid path or a step that cannot select a location.
	ErrPath = errors.New("invalid path")
	// ErrType identifies an incompatible root, input or requested result type.
	ErrType = errors.New("incompatible type")
	// ErrMissing identifies an absent location required by a typed read or write.
	ErrMissing = errors.New("missing value")
	// ErrNil identifies a nil intermediate that a write cannot traverse or allocate.
	ErrNil = errors.New("nil intermediate")
	// ErrAccess identifies a value that safe reflection cannot read or write.
	ErrAccess = errors.New("inaccessible value")
	// ErrIndex identifies an index outside an existing slice or array.
	ErrIndex = errors.New("index out of bounds")
	// ErrKey identifies an incompatible, non-comparable or non-reflexive map key.
	ErrKey = errors.New("unsupported map key")
	// ErrLimit identifies an exceeded compilation, access or walk budget.
	ErrLimit = errors.New("limit exceeded")
	// ErrConversion identifies a numeric conversion that is unsupported or loses information.
	ErrConversion = errors.New("lossy or unsupported conversion")
	// ErrCallback identifies an error returned by a walk visitor.
	ErrCallback = errors.New("callback failed")
)

// Error describes a failed operation. Its text names the operation, the Go type
// involved and the cause, such as a resolver's error or a [NameConflict]. It
// never includes path steps, values or struct tags, because map keys, values
// and tag values such as defaults can be caller data or secrets; Path and Step
// carry the location for caller-controlled display, and Type the full type.
// Step is the zero-based path step that failed, or -1 for a root/schema error.
// TypeRoute identifies a schema location for type walks, without inventing a
// live map key or collection index.
type Error struct {
	Op        string
	Kind      error
	Path      path.Path
	Step      int
	Type      reflect.Type
	Cause     error
	TypeRoute []TypeEdge
}

// NameConflict reports two fields of Owner claiming equivalent names. Its text
// names both Go fields and the conflicting name, which come from the type's
// declaration, not from caller values. Name is the claim compilation rejected:
// Second's name or alias, already used by First's.
type NameConflict struct {
	Owner         reflect.Type
	Name          string
	First, Second *Field

	firstAlias, secondAlias bool
}

func (e *NameConflict) Error() string {
	first, second, owner := fieldLabel(e.First), fieldLabel(e.Second), TypeText(e.Owner)
	switch {
	case !e.firstAlias && !e.secondAlias:
		return fmt.Sprintf("fields %s and %s of %s both use the name %q", first, second, owner, e.Name)
	case e.firstAlias && e.secondAlias:
		return fmt.Sprintf("fields %s and %s of %s both have the alias %q", first, second, owner, e.Name)
	case e.secondAlias:
		return fmt.Sprintf("field %s of %s has the alias %q, which is the name of field %s", second, owner, e.Name, first)
	default:
		return fmt.Sprintf("field %s of %s uses the name %q, which is an alias of field %s", second, owner, e.Name, first)
	}
}

// TypeText is t's Go syntax as reflect prints it, without struct tags, for
// error text: reflect prints an unnamed struct type's tags, in its own text and
// in a generic type's arguments, and a tag value can hold a secret, such as a
// default. Every error gowalker formats prints its types this way. A nil t is
// "<nil>".
func TypeText(t reflect.Type) string {
	if t == nil {
		return "<nil>"
	}
	text := t.String()
	if !strings.Contains(text, `"`) {
		return text
	}
	// In a type's text a double quote only opens a tag, which reflect writes
	// as a space and a Go-quoted string after the field's type.
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] == ' ' && i+1 < len(text) && text[i+1] == '"' {
			i += 2
			for i < len(text) && text[i] != '"' {
				if text[i] == '\\' {
					i++
				}
				i++
			}
			continue
		}
		b.WriteByte(text[i])
	}
	return b.String()
}

// fieldLabel is a field's Go name, prefixed by the embedded fields it was
// promoted through, so two promoted ID fields read as Audit.ID and Base.ID.
func fieldLabel(f *Field) string {
	var b strings.Builder
	for _, parent := range f.parents {
		b.WriteString(parent.Name)
		b.WriteByte('.')
	}
	b.WriteString(f.goField.Name)
	return b.String()
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("gowalker: ")
	b.WriteString(e.Op)
	if e.Type != nil {
		b.WriteByte(' ')
		b.WriteString(TypeText(e.Type))
	}
	b.WriteString(": ")
	b.WriteString(e.Kind.Error())
	if e.Cause != nil {
		b.WriteString(": ")
		b.WriteString(e.Cause.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error        { return e.Cause }
func (e *Error) Is(target error) bool { return target == e.Kind }

func failure(op string, kind error, t reflect.Type, p path.Path, step int, cause error) error {
	if nested, ok := kind.(*Error); ok {
		if cause == nil {
			cause = nested
		}
		kind = nested.Kind
	}
	return &Error{Op: op, Kind: kind, Type: t, Path: p, Step: step, Cause: cause}
}
