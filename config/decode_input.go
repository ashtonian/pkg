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
		return md, decodeErrors(root, tree, err)
	}
	return md, nil
}
