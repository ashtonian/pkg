package kongprovider

import (
	"errors"

	"github.com/alecthomas/kong"

	"github.com/ashtonian/pkg/config"
)

// kongDefaultNegation is the value Kong gives a flag's Tag.Negatable for a
// bare `negatable:""`: the flag negates as --no-<name>. Kong keeps the
// constant unexported; a test pins it to Kong's help.
const kongDefaultNegation = "_"

// UsageFlags returns a config.PrintOption that adds a Flag column to
// config.PrintUsage's table and Markdown: for each key, the flag of the CLI k
// built that writes that field of cfg, named as Kong's help names it —
// --db-host, -p, --port, --[no-]metrics. A key no flag sets has an empty
// cell: a field tagged kong:"-", one whose flag is hidden, a map entry's or
// list element's row. cfg is the config struct inside the CLI struct k was
// built from, as [HelpFromConfig] takes it, and T the config PrintUsage
// describes:
//
//	var cli CLI
//	parser, err := kong.New(&cli)
//	// ...
//	flags, err := kongprovider.UsageFlags(parser, &cli.Serve.AppConfig)
//	// ...
//	err = config.PrintUsage[AppConfig]("APP", config.WithFormat(config.FormatMarkdown), flags)
//
// It fails as HelpFromConfig does: with config.ErrSchema on an invalid field
// catalog, config.ErrKey on ambiguous field addresses, and an error when no
// flag of the CLI writes into cfg.
func UsageFlags[T any](k *kong.Kong, cfg *T) (config.PrintOption, error) {
	if k == nil {
		return nil, errors.New("kongprovider: nil Kong parser")
	}
	if cfg == nil {
		return nil, errors.New("kongprovider: nil config")
	}
	fields, err := fieldsOfFlags(cfg, allFlags(k.Model.Node))
	if err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return nil, errNoFlag(k.Model.Name)
	}
	names := make(map[string]string, len(fields))
	for fl, f := range fields {
		if !fl.Hidden {
			names[f.Path] = flagUsage(fl)
		}
	}
	return config.WithUsageColumn("Flag", func(e config.UsageEntry) string { return names[e.Key] }), nil
}

// flagUsage names fl as Kong's help does, without its placeholder: its short
// name, then its long one, and for a negatable boolean its negation.
func flagUsage(fl *kong.Flag) string {
	name := fl.Name
	if fl.IsBool() {
		switch fl.Tag.Negatable {
		case "":
		case kongDefaultNegation:
			name = "[no-]" + name
		default:
			name += "/" + fl.Tag.Negatable
		}
	}
	text := "--" + name
	if fl.Short != 0 {
		text = "-" + string(fl.Short) + ", " + text
	}
	return text
}
