package config

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"

	"github.com/ashtonian/pkg/gowalker"
)

// KeyError distinguishes an unknown key (Unknown, ErrKey) from a known key
// that cannot be set as written or holds an invalid value (ErrValidation).
// Two refusals of what is no key naming one value are Unknown too, and
// their messages say which they are: a key holding [*] where one naming a
// single value is required (key vault[*] is not concrete), and text that
// does not parse as a key (key "pool[x]" does not parse: …).
// Key is the key as diagnostics may show it: cut short where the keys of a
// sensitive map, list or interface, or those a sensitive remain field
// collects, begin; for text that does not parse, the text redacted (see
// RedactText), which the message quotes, saying why only when redacting
// left it as it was.
// Error, and Err's own text, name keys as Key does and never a value; a
// reason that would name what Key withholds is not shown. Err keeps a
// decoder's or validator's cause for trusted diagnostics, and the errors
// beneath it, a validator's own among them, may name the whole key and hold
// the value.
type KeyError struct {
	Key     string `json:"key"`
	Unknown bool   `json:"unknown,omitempty"`
	Err     error  `json:"-"`
	// suggestion is the key an unknown one most likely misspells.
	suggestion string
	// form is what Key is when it is no key naming one value.
	form keyForm
	// reason is what Error says of Key beyond its category, set where the
	// KeyError is made: why Key is no key naming one value, or why a known
	// key cannot be set as written.
	reason string
}

// keyForm is the shape of a refused key's text: a key naming one value,
// whether or not config reads it, or text naming no one value.
type keyForm uint8

const (
	formConcrete keyForm = iota // a key naming one value
	formPattern                 // a key holding [*] (see notConcrete)
	formUnparsed                // text that does not parse as a key (see unparsedKeyError)
)

func (e *KeyError) Error() string {
	switch {
	case e.form == formUnparsed:
		return fmt.Sprintf("config: key %q %s", e.Key, e.reason)
	case e.form == formPattern:
		return fmt.Sprintf("config: key %s %s", e.Key, e.reason)
	case e.Unknown:
		return fmt.Sprintf("config: unknown key %s", e.Key)
	case e.reason != "":
		return fmt.Sprintf("config: key %s: %s", e.Key, e.reason)
	}
	return fmt.Sprintf("config: invalid value for key %s", e.Key)
}

func (e *KeyError) Unwrap() error { return e.Err }

func (e *KeyError) Is(target error) bool {
	return target == ErrKey && e.Unknown || target == ErrValidation && !e.Unknown
}

// namesNothing reports whether e is an unknown key: one naming one value,
// though not one config reads.
func (e *KeyError) namesNothing() bool {
	return e.Unknown && e.form == formConcrete
}

// A *KeyError is made only here: newKeyError for a key that parses,
// unparsedKeyError for one that does not, unknownKeyError for a key a walk
// found inside a value. Each words Err as it words Key.

// newKeyError is the *KeyError for key: Key is key as diagnostics may show
// it (see shownKey), and a keyFault cause becomes the keyProblem it words
// the same way, its reason withheld when it names a key within sensitive
// contents. Any other cause is a decoder's or validator's, which this
// package words that way where it wraps it (see withhold and
// ruleViolation); Err keeps it whole.
func newKeyError(schema *schemaNode, key keyPath, unknown bool, cause error) *KeyError {
	shown := shownPath(schema, key)
	ke := &KeyError{Key: shown.orRoot(), Unknown: unknown, Err: cause}
	var f *keyFault
	if !errors.As(cause, &f) {
		return ke
	}
	problem := &keyProblem{unknown: f.unknown, key: ke.Key, reason: f.reason()}
	if f.namesWithheld(schema) {
		problem.reason = withheldReason
	}
	ke.Err = problem
	switch {
	case f.pattern && shown.isPattern():
		ke.form, ke.reason = formPattern, "is not concrete"
	case f.pattern:
		ke.form, ke.reason = formPattern, "holds [*] within its sensitive contents, so it is not concrete"
	case !f.unknown:
		ke.reason = problem.reason
	}
	if f.suggestion != nil && !withholds(schema, f.suggestion) {
		ke.suggestion = f.suggestion.String()
	}
	return ke
}

// withheldReason stands in for a reason naming a key within sensitive
// contents. Only a key within them can have one, so Key names where they
// begin.
const withheldReason = "the reason names sensitive contents, which are not shown"

// unparsedKeyError is the *KeyError for key, which does not parse (err, a
// *keySyntaxError): Key is key redacted. Its reason quotes parts of key, so
// when redacting changed key, the message and Err drop it.
func unparsedKeyError(key string, err error) *KeyError {
	shown := RedactText(key)
	ke := &KeyError{Key: shown, Unknown: true, Err: err, form: formUnparsed, reason: "does not parse"}
	var syntax *keySyntaxError
	switch {
	case shown != key:
		ke.Err = &keyProblem{unknown: true, key: strconv.Quote(shown), reason: "it does not parse, and the reason quotes it, so it is not shown"}
	case errors.As(err, &syntax):
		ke.reason += ": " + syntax.reason
	}
	return ke
}

// unknownKeyError is the *KeyError for u, a key inside a value that no
// field reads, which the walk that found it named as diagnostics may show it
// (see keyWalk and unusedKey).
func unknownKeyError(u UnknownKey) *KeyError {
	ke := &KeyError{Key: u.Key, Unknown: true, Err: &keyProblem{unknown: true, key: u.Key, reason: "no field reads it"}, suggestion: u.Suggestion}
	return ke
}

// shownKey is key as diagnostics may show it, the root as $.
func shownKey(schema *schemaNode, key keyPath) string {
	return shownPath(schema, key).orRoot()
}

// shownPath is key as diagnostics may show it (see diagnosticPath), and a
// [*] where they cut it short: [*] names no key the input holds.
func shownPath(schema *schemaNode, key keyPath) keyPath {
	shown := diagnosticPath(schema, key)
	if len(shown) < len(key) && key[len(shown)].kind == segAny {
		shown = key[:len(shown)+1]
	}
	return shown
}

// withholds reports whether diagnostics cut key short.
func withholds(schema *schemaNode, key keyPath) bool {
	return len(diagnosticPath(schema, key)) < len(key)
}

// keyFault is why config reads nothing at a key (ErrKey) or cannot set it
// as written (ErrValidation), not yet worded for a *KeyError: every key its
// reason names is an argument, never text. Its own Error writes them as
// given, for a caller that gave the key (FieldFor); newKeyError writes it as
// diagnostics may show it.
type keyFault struct {
	unknown bool    // ErrKey, not ErrValidation (see keySentinel)
	key     keyPath // the key an ErrKey fault's Error names
	format  string  // the reason, a verb for each of args
	args    []faultArg
	// suggestion is the key an unknown field most likely misspells.
	suggestion keyPath
	pattern    bool // the key holds [*] where it must name one value
}

// notRead is the fault of key, which config reads nothing at.
func notRead(key keyPath, format string, args ...faultArg) *keyFault {
	fault := &keyFault{unknown: true, key: key, format: format, args: args}
	return fault
}

// notConcrete is the fault of key, which holds [*] where it must name one
// value: an unknown key, which its KeyError's message tells apart.
func notConcrete(key keyPath) *keyFault {
	f := notRead(key, "key must be concrete")
	f.pattern = true
	return f
}

// refuse is the fault of a key that cannot be set as written.
func refuse(format string, args ...faultArg) *keyFault {
	fault := &keyFault{format: format, args: args}
	return fault
}

func (f *keyFault) Error() string {
	if f.unknown {
		return fmt.Sprintf("%s %s: %s", ErrKey, f.key.orRoot(), f.reason())
	}
	return fmt.Sprintf("%s: %s", ErrValidation, f.reason())
}

func (f *keyFault) Unwrap() error { return keySentinel(f.unknown) }

// keySentinel is the category of a refused key: ErrKey when config reads
// nothing at it, ErrValidation when it cannot be set as written.
func keySentinel(unknown bool) error {
	if unknown {
		return ErrKey
	}
	return ErrValidation
}

func (f *keyFault) reason() string {
	texts := make([]any, len(f.args))
	for i, a := range f.args {
		texts[i] = a.faultText()
	}
	return fmt.Sprintf(f.format, texts...)
}

// namesWithheld reports whether f's reason names a key diagnostics cut
// short.
func (f *keyFault) namesWithheld(schema *schemaNode) bool {
	for _, a := range f.args {
		key, ok := a.faultKey()
		if ok && withholds(schema, key) {
			return true
		}
	}
	return false
}

// faultArg is what a keyFault's reason may name: a key (keyPath), the field
// a key names (fieldName), a type (typeText), a number of elements
// (elementCount), or text already worded as diagnostics may show it
// (shownText). Nothing else is one, so input reaches a reason only as a key
// newKeyError can check.
type faultArg interface {
	faultText() string
	faultKey() (keyPath, bool) // the key it names, if any
}

func (p keyPath) faultText() string         { return p.orRoot() }
func (p keyPath) faultKey() (keyPath, bool) { return p, true }

// fieldName names the field its key ends in, quoted.
type fieldName keyPath

func (f fieldName) faultText() string         { return strconv.Quote(f[len(f)-1].name) }
func (f fieldName) faultKey() (keyPath, bool) { return keyPath(f), true }

// typeText names a type as gowalker writes it.
type typeText struct{ t reflect.Type }

func (t typeText) faultText() string       { return gowalker.TypeText(t.t) }
func (typeText) faultKey() (keyPath, bool) { return nil, false }

type elementCount int

func (n elementCount) faultText() string       { return strconv.Itoa(int(n)) }
func (elementCount) faultKey() (keyPath, bool) { return nil, false }

// shownText is text already worded as diagnostics may show it: a fixed
// word, or the keys a key walk lists (see keyWalk).
type shownText string

func (s shownText) faultText() string       { return string(s) }
func (shownText) faultKey() (keyPath, bool) { return nil, false }

// keyProblem is a keyFault as newKeyError words it: text only, every key in
// it as diagnostics may show it.
type keyProblem struct {
	unknown bool   // ErrKey, not ErrValidation (see keySentinel)
	key     string // the key an ErrKey problem names
	reason  string
}

func (p *keyProblem) Error() string {
	if p.unknown {
		return fmt.Sprintf("%s %s: %s", ErrKey, p.key, p.reason)
	}
	return fmt.Sprintf("%s: %s", ErrValidation, p.reason)
}

func (p *keyProblem) Unwrap() error { return keySentinel(p.unknown) }
