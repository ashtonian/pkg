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
		return md, &decodeFailures{err: decodeErrors(root, tree, err), at: failedKeys(root, tree, err)}
	}
	return md, nil
}

// decodeFailures is a decode's failure, err as decodeErrors rewrites it,
// with the key of each failure it holds, which WithInvalidEntries
// attributes to the entries setting it. It adds no text, and errors.As
// still reaches what err holds.
type decodeFailures struct {
	err error
	at  []keyPath
}

func (e *decodeFailures) Error() string { return e.err.Error() }
func (e *decodeFailures) Unwrap() error { return e.err }

// failedKeys is the key of every *mapstructure.DecodeError in err, a
// decode of tree: the one treeKey proves from the decoder's name for it,
// exact or the nearest it can prove.
func failedKeys(root *schemaNode, tree any, err error) []keyPath {
	var at []keyPath
	var walk func(error)
	walk = func(e error) {
		switch u := e.(type) {
		case *mapstructure.DecodeError:
			p, _, _, _ := treeKey(root, tree, u.Name())
			at = append(at, p)
		case interface{ Unwrap() []error }:
			for _, inner := range u.Unwrap() {
				walk(inner)
			}
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		}
	}
	walk(err)
	return at
}
