package config

import (
	"github.com/go-viper/mapstructure/v2"
)

func (o *options) decodeInput(root *schemaNode, tree map[string]any, out any, observe func(mapstructure.DecodeHookFunc) mapstructure.DecodeHookFunc) (mapstructure.Metadata, error) {
	var md mapstructure.Metadata
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		DecodeHook: o.decodeHook(observe), WeaklyTypedInput: true,
		Result: out, TagName: "koanf", Metadata: &md,
	})
	if err != nil {
		return md, err
	}
	if err := decoder.Decode(tree); err != nil {
		failures := &decodeFailures{err: decodeErrors(root, tree, err), each: placeDecodeFailures(root, tree, err)}
		return md, failures
	}
	return md, nil
}

// decodeFailures is a decode's failure, err as decodeErrors rewrites it,
// with each failure it holds placed at its key, which WithInvalidEntries
// attributes to the entry setting that key. It adds no text, and errors.As
// still reaches what err holds.
type decodeFailures struct {
	err  error
	each []placedFailure
}

func (e *decodeFailures) Error() string { return e.err.Error() }
func (e *decodeFailures) Unwrap() error { return e.err }

// placedFailure is one *mapstructure.DecodeError of a decode: the key
// treeKey proves from the decoder's name for it, whether that key is all of
// the name, and the error as decodeErrors rewrites it.
type placedFailure struct {
	at    keyPath
	exact bool
	err   error
}

// placeDecodeFailures places every *mapstructure.DecodeError in err, a
// decode of tree.
func placeDecodeFailures(root *schemaNode, tree any, err error) []placedFailure {
	var out []placedFailure
	var walk func(error)
	walk = func(e error) {
		failure, ok := e.(*mapstructure.DecodeError) //nolint:errorlint // visits every node of the tree; errors.As stops at the first match
		if ok {
			p, _, _, exact := treeKey(root, tree, failure.Name())
			rewritten, _ := withhold(root, tree, failure)
			out = append(out, placedFailure{at: p, exact: exact, err: rewritten})
			return
		}
		joined, ok := e.(interface{ Unwrap() []error })
		if ok {
			for _, inner := range joined.Unwrap() {
				walk(inner)
			}
			return
		}
		wrapped, ok := e.(interface{ Unwrap() error })
		if ok {
			walk(wrapped.Unwrap())
		}
	}
	walk(err)
	return out
}
