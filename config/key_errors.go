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
// Key is the key as diagnostics may show it: cut short where the keys of a
// sensitive map, list or interface, or those a sensitive remain field
// collects, begin, and redacted (see RedactText) when it does not parse.
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
}

func (e *KeyError) Error() string {
	var problem *keyProblem
	switch {
	case e.Unknown:
		return fmt.Sprintf("config: unknown key %s", e.Key)
	case errors.As(e.Err, &problem):
		return fmt.Sprintf("config: key %s: %s", e.Key, problem.reason)
	}
	return fmt.Sprintf("config: invalid value for key %s", e.Key)
}

func (e *KeyError) Unwrap() error { return e.Err }

func (e *KeyError) Is(target error) bool {
	return target == ErrKey && e.Unknown || target == ErrValidation && !e.Unknown
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
	ke := &KeyError{Key: shownKey(schema, key), Unknown: unknown, Err: cause}
	f, ok := cause.(*keyFault)
	if !ok {
		return ke
	}
	problem := &keyProblem{sentinel: f.sentinel, key: ke.Key, reason: f.reason()}
	if f.namesWithheld(schema) {
		problem.reason = withheldReason
	}
	ke.Err = problem
	if f.suggestion != nil && !withholds(schema, f.suggestion) {
		ke.suggestion = f.suggestion.String()
	}
	return ke
}

// withheldReason stands in for a reason naming a key within sensitive
// contents. Only a key within them can have one, so Key names where they
// begin.
const withheldReason = "the reason names sensitive contents, which are not shown"

// unparsedKeyError is the *KeyError for key, which does not parse (err):
// Key is key redacted. When redacting changed it, Err drops err's reason,
// which quotes the key.
func unparsedKeyError(key string, err error) *KeyError {
	shown := RedactText(key)
	if shown != key {
		err = &keyProblem{sentinel: ErrKey, key: shown, reason: "it does not parse, and the reason quotes it, so it is not shown"}
	}
	return &KeyError{Key: shown, Unknown: true, Err: err}
}

// unknownKeyError is the *KeyError for u, a key inside a value that no
// field reads, which the walk that found it named as diagnostics may show it
// (see keyWalk and unusedKey).
func unknownKeyError(u UnknownKey) *KeyError {
	return &KeyError{Key: u.Key, Unknown: true, Err: &keyProblem{sentinel: ErrKey, key: u.Key, reason: "no field reads it"}, suggestion: u.Suggestion}
}

// shownKey is key as diagnostics may show it (see diagnosticPath), the root
// as $.
func shownKey(schema *schemaNode, key keyPath) string {
	return diagnosticPath(schema, key).orRoot()
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
	sentinel error   // ErrKey or ErrValidation
	key      keyPath // the key an ErrKey fault's Error names
	format   string  // the reason, a verb for each of args
	args     []faultArg
	// suggestion is the key an unknown field most likely misspells.
	suggestion keyPath
}

// notRead is the fault of key, which config reads nothing at.
func notRead(key keyPath, format string, args ...faultArg) *keyFault {
	return &keyFault{sentinel: ErrKey, key: key, format: format, args: args}
}

// refuse is the fault of a key that cannot be set as written.
func refuse(format string, args ...faultArg) *keyFault {
	return &keyFault{sentinel: ErrValidation, format: format, args: args}
}

func (f *keyFault) Error() string {
	if f.sentinel == ErrKey {
		return fmt.Sprintf("%s %s: %s", ErrKey, f.key.orRoot(), f.reason())
	}
	return fmt.Sprintf("%s: %s", f.sentinel, f.reason())
}

func (f *keyFault) Unwrap() error { return f.sentinel }

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
		if key, ok := a.faultKey(); ok && withholds(schema, key) {
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
	sentinel error  // ErrKey or ErrValidation
	key      string // the key an ErrKey problem names
	reason   string
}

func (p *keyProblem) Error() string {
	if p.sentinel == ErrKey {
		return fmt.Sprintf("%s %s: %s", ErrKey, p.key, p.reason)
	}
	return fmt.Sprintf("%s: %s", p.sentinel, p.reason)
}

func (p *keyProblem) Unwrap() error { return p.sentinel }
