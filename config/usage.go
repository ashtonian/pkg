package config

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
)

// EnvVar is the environment variable the environment layer reads for f under
// prefix — a prefix [WithEnvPrefix] or [WithEnvPrefixes] was given, with or
// without its trailing "_". It is empty when prefix is.
func (f Field) EnvVar(prefix string) string {
	if list := envPrefixList([]string{prefix}); len(list) > 0 {
		return envName(list[0], f.Path)
	}
	return ""
}

// EnvVars returns every variable the environment layer reads for f under
// prefixes, as [WithEnvPrefixes] takes them, the first winning when several
// are set: [Field.EnvVar] under each prefix, most specific first, then the
// names its env tag lists, read as written. With no prefix the environment
// is not read, and there are none.
func (f Field) EnvVars(prefixes ...string) []string {
	list := envPrefixList(prefixes)
	if len(list) == 0 {
		return nil
	}
	names := make([]string, 0, len(list))
	for _, p := range list {
		names = append(names, envName(p, f.Path))
	}
	return append(names, envTagNames(f.Tag)...)
}

// PrintableExample returns f's `example` tag and true when diagnostics may
// print it: under the rule [Field.PrintableDefault] applies to a default.
func (f Field) PrintableExample() (string, bool) {
	ex, ok := f.Tag.Lookup("example")
	if !ok || f.Sensitive || mayHoldSensitive(f.Type, map[reflect.Type]bool{}) || RedactText(ex) != ex {
		return "", false
	}
	return ex, true
}

// PrintableDefault returns f's `default` tag and true when diagnostics may
// print it, and false when f has no default or it must be withheld: f is
// Sensitive, or its type could hold a sensitive value beneath it — a map or
// interface, whose entries only a decoded value reveals, or a struct, slice
// or array holding a sensitive field or one of those.
func (f Field) PrintableDefault() (string, bool) {
	def, ok := f.Tag.Lookup("default")
	if !ok || f.Sensitive || mayHoldSensitive(f.Type, map[reflect.Type]bool{}) || RedactText(def) != def {
		return "", false
	}
	return def, true
}

// UsageEntry is one key an operator can set, with safe printable metadata.
// Field retains the original tags for custom generators and is omitted from JSON.
type UsageEntry struct {
	EnvVars     []string      `json:"env_vars,omitempty"`
	Allowed     []string      `json:"allowed,omitempty"`
	Bounds      []UsageBounds `json:"bounds,omitempty"`
	Deprecated  []string      `json:"deprecated,omitempty"`
	Withheld    bool          `json:"withheld,omitempty"`
	Reload      string        `json:"reload"`
	Field       Field         `json:"-"`
	Key         string        `json:"key"`
	Env         string        `json:"env,omitempty"`      // the variable under the first prefix
	AlsoEnv     []string      `json:"also_env,omitempty"` // the rest of EnvVars: under broader prefixes, then an env tag's
	JSON        bool          `json:"json,omitempty"`     // a variable sets it as JSON
	Type        string        `json:"type"`
	Default     string        `json:"default,omitempty"`
	Example     string        `json:"example,omitempty"`
	Required    bool          `json:"required,omitempty"`
	Sensitive   bool          `json:"sensitive,omitempty"`
	Description string        `json:"description,omitempty"`
	// pattern marks a map entry or list element: Key holds [*], and Env a
	// <KEY> placeholder or nothing.
	pattern    bool
	hasDefault bool
}

type usageRow = UsageEntry

// Usage returns the catalog rendered by PrintUsage, in declaration order,
// naming each key's variables under envPrefixes as [WithEnvPrefixes] takes
// them: Env is the one under the first prefix, AlsoEnv the rest in
// precedence order, and EnvVars all of them. Defaults that are sensitive or
// could contain secrets are empty and Withheld is true. Allowed follows
// JSONSchema's enum and withholding rules. Collection entries use [*]
// patterns and environment placeholders where supported.
func Usage[T any](envPrefixes ...string) ([]UsageEntry, error) {
	root, err := rootMetadata(reflect.TypeFor[T]())
	if err != nil {
		return nil, err
	}
	var defaults declaredDefaults
	_, ownsDefaults := reflect.New(root.typ).Interface().(DefaultProvider)
	if ownsDefaults {
		defaults, err = readDefaults(root.typ, true)
		if err != nil {
			return nil, err
		}
	}
	rows, err := usageRows(root, envPrefixList(envPrefixes), defaults)
	if err != nil {
		return nil, err
	}
	if !ownsDefaults {
		return rows, nil
	}
	for i := range rows {
		r := &rows[i]
		path, err := parseKeyPath(r.Key)
		if err != nil {
			return nil, err
		}
		if r.pattern {
			continue
		}
		if !defaults.has(path) {
			r.Default, r.hasDefault, r.Withheld = "", false, false
			continue
		}
		r.hasDefault = true
		if r.Sensitive || mayHoldSensitive(r.Field.Type, map[reflect.Type]bool{}) {
			r.Default, r.Withheld = "", true
			continue
		}
		r.Default, r.Withheld, err = defaultText(defaults.field(root, path))
		if err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// PrintUsage writes every key of T an operator can set: its path, the
// environment variables that set it under envPrefix and the
// [WithFallbackEnvPrefixes] prefixes in precedence order (none when there is
// no prefix), its type, its `default` tag, whether `validate:"required"`
// makes it mandatory, and its `desc` tag. A map or list one variable can set whole,
// or one with a default, has a row of its own. A map's entries are one row
// per element field, keyed by the pattern features[*].limit with a <KEY>
// placeholder in the variable, and a slice of structs' elements one row per
// element field, items[*].port, with no variable. Tables mark withheld defaults
// as [REDACTED]; JSON omits default and sets withheld (see [Field.PrintableDefault]).
//
// [WithFormat] selects a table (the default), JSON (an array of objects),
// FormatMarkdown (an escaped Markdown table), or
// FormatEnv: a template of `export NAME='default'` lines under the first
// prefix, with a key that has no default, a sensitive key and a map entry
// written as a commented-out line; FormatEnv needs an envPrefix.
// [WithWriter], [WithSortByName] and WithFallbackEnvPrefixes apply;
// descriptions are always included.
func PrintUsage[T any](envPrefix string, opts ...PrintOption) error {
	po := defaultPrintOptions()
	for _, opt := range opts {
		opt(&po)
	}
	prefixes := po.envPrefixes(envPrefix)
	rows, err := Usage[T](prefixes...)
	if err != nil {
		return err
	}
	withEnv := len(prefixes) > 0
	if po.sortByName {
		slices.SortStableFunc(rows, func(a, b usageRow) int { return cmp.Compare(a.Key, b.Key) })
	}
	switch po.formatOr(FormatTable) {
	case FormatTable:
		return printUsageTable(po.writer, rows, withEnv)
	case FormatJSON:
		enc := json.NewEncoder(po.writer)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	case FormatMarkdown:
		return printUsageMarkdown(po.writer, rows, withEnv)
	case FormatEnv:
		if !withEnv {
			return errors.New("config: usage in FormatEnv needs an environment prefix")
		}
		return printUsageEnv(po.writer, rows)
	default:
		return fmt.Errorf("config: unknown print format %q (want %q, %q, %q or %q)",
			po.format, FormatTable, FormatJSON, FormatMarkdown, FormatEnv)
	}
}

// usageRows describes every key of root, a map's entries through its
// element: a sensitive map covers all of them.
func usageRows(root *schemaNode, prefixes []string, defaults declaredDefaults) ([]UsageEntry, error) {
	var rows []UsageEntry
	var resultErr error
	add := func(f Field, key string, env envNames, pattern bool) {
		if resultErr != nil {
			return
		}
		row, err := usageRowOf(f, key, env)
		if err != nil {
			resultErr = err
			return
		}
		row.pattern = pattern
		rows = append(rows, row)
	}
	walking := map[*schemaNode]bool{}
	var walk func(*schemaNode, cursor, envNames, bool)
	walk = func(n *schemaNode, c cursor, env envNames, pattern bool) {
		if resultErr != nil || walking[n] {
			return
		}
		walking[n] = true
		defer delete(walking, n)
		switch n.kind {
		case nodeBranch:
			for _, f := range n.fields {
				next := c.intoField(f)
				fieldEnv := env.field(f.key)
				if f.node.kind == nodeBranch && jsonFormat(f.sf.Tag) {
					add(next.owner, next.path.String(), fieldEnv, pattern)
				}
				walk(f.node, next, fieldEnv, pattern)
			}
		case nodeMap, nodeList:
			_, has := c.owner.Tag.Lookup("default")
			expand := n.kind == nodeMap || n.elem.kind == nodeBranch || n.elem.kind == nodeMap || n.elem.kind == nodeList
			if has || defaults.has(c.path) || variableSetsField(c.owner.Tag, c.owner.Type) || !expand {
				add(c.owner, c.path.String(), env, pattern)
			}
			if !expand {
				return
			}
			var elemEnv envNames // a list element has no variable
			if n.kind == nodeMap {
				elemEnv = env.entries(n.typ)
			}
			next := c.intoEntry(segment{kind: segAny}, n.typ.Elem())
			if n.elem.kind != nodeBranch {
				next.owner.Tag = reflect.StructTag(fmt.Sprintf("desc:%q", c.owner.Tag.Get("desc")))
			}
			walk(n.elem, next, elemEnv, true)
		default:
			add(c.owner, c.path.String(), env, pattern)
		}
	}
	walk(root, cursor{}, rootEnvNames(prefixes), false)
	return rows, resultErr
}

// usageRowOf describes f, shown as key, at env.
func usageRowOf(f Field, key string, env envNames) (usageRow, error) {
	row := usageRow{
		Key:         key,
		Type:        typeText(f.Type),
		Required:    requiredBeforeOmission(f.Tag.Get("validate")),
		Sensitive:   f.Sensitive,
		Description: f.Tag.Get("desc"),
		Field:       cloneField(f), Reload: f.Reload, Deprecated: aliasNames(f.Tag),
	}
	var err error
	row.Allowed, row.Bounds, err = usageConstraints(f)
	if err != nil {
		return usageRow{}, err
	}
	if names := env.set(f.Tag, f.Type); len(names) > 0 {
		row.EnvVars, row.Env = names, names[0]
		if len(names) > 1 {
			row.AlsoEnv = slices.Clone(names[1:])
		}
	}
	row.JSON = jsonFormat(f.Tag)
	if ex, ok := f.PrintableExample(); ok {
		row.Example = ex
	}
	def, has := f.Tag.Lookup("default")
	row.hasDefault = has
	if has {
		row.Default = def
		if _, printable := f.PrintableDefault(); !printable {
			row.Default = ""
			row.Withheld = true
		}
	}
	return row, nil
}

func printUsageTable(w io.Writer, rows []usageRow, withEnv bool) error {
	defaultText := func(r usageRow) string {
		text := r.Default
		if r.Withheld {
			text = redactedValue
		}
		if !r.hasDefault && r.Required {
			text = "(required)"
		}
		if !r.hasDefault && r.Example != "" {
			text = strings.TrimSpace(text + " e.g. " + r.Example)
		}
		return text
	}
	envText := func(r usageRow) string {
		text := strings.Join(r.EnvVars, ", ")
		if r.JSON && r.Env != "" {
			text += " (JSON)"
		}
		return text
	}
	reloadW := len("RELOAD")
	for _, r := range rows {
		reloadW = max(reloadW, len(r.Reload))
	}
	keyW, envW, typeW, defW := len("KEY"), len("ENV"), len("TYPE"), len("DEFAULT")
	for _, r := range rows {
		keyW = max(keyW, len(r.Key))
		envW = max(envW, len(envText(r)))
		typeW = max(typeW, len(r.Type))
		defW = max(defW, len(defaultText(r)))
	}
	ew := &errWriter{w: w}
	if withEnv {
		row := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%-%ds  %%-%ds  %%s\n", keyW, envW, typeW, defW, reloadW)
		ew.printf(row, "KEY", "ENV", "TYPE", "DEFAULT", "RELOAD", "DESCRIPTION")
		for _, r := range rows {
			ew.printf(row, r.Key, envText(r), r.Type, defaultText(r), r.Reload, r.Description)
		}
		return ew.err
	}
	row := fmt.Sprintf("%%-%ds  %%-%ds  %%-%ds  %%-%ds  %%s\n", keyW, typeW, defW, reloadW)
	ew.printf(row, "KEY", "TYPE", "DEFAULT", "RELOAD", "DESCRIPTION")
	for _, r := range rows {
		ew.printf(row, r.Key, r.Type, defaultText(r), r.Reload, r.Description)
	}
	return ew.err
}

func printUsageEnv(w io.Writer, rows []usageRow) error {
	ew := &errWriter{w: w}
	for i, r := range rows {
		if i > 0 {
			ew.printf("\n")
		}
		if r.Description != "" {
			ew.printf("# %s\n", strings.ReplaceAll(r.Description, "\n", " "))
		}
		note := r.Key + " (" + r.Type + ")"
		if r.Required {
			note += ", required"
		}
		if r.JSON {
			note += ", JSON"
		}
		ew.printf("# %s\n", note)
		if r.Env == "" {
			ew.printf("# set in a config file\n")
			continue
		}
		if r.pattern || r.Sensitive || r.Withheld || !r.hasDefault {
			example := ""
			if r.Example != "" {
				example = shellQuote(r.Example)
			}
			ew.printf("# %s\n", strings.ReplaceAll("export "+r.Env+"="+example, "\n", "\n# "))
			continue
		}
		ew.printf("export %s=%s\n", r.Env, shellQuote(r.Default))
	}
	return ew.err
}

func printUsageMarkdown(w io.Writer, rows []UsageEntry, withEnv bool) error {
	ew := &errWriter{w: w}
	if withEnv {
		ew.printf("| Key | Environment | Type | Default | Bounds | Reload | Description |\n| --- | --- | --- | --- | --- | --- | --- |\n")
	} else {
		ew.printf("| Key | Type | Default | Bounds | Reload | Description |\n| --- | --- | --- | --- | --- | --- |\n")
	}
	for _, r := range rows {
		def := r.Default
		if r.Withheld {
			def = redactedValue
		} else if !r.hasDefault && r.Required {
			def = "(required)"
		}
		cells := []string{r.Key}
		if withEnv {
			cells = append(cells, strings.Join(r.EnvVars, ", "))
		}
		cells = append(cells, r.Type, def, usageBoundsText(r.Bounds), r.Reload, r.Description)
		for i := range cells {
			cells[i] = markdownCell(cells[i])
		}
		ew.printf("| %s |\n", strings.Join(cells, " | "))
	}
	return ew.err
}

func markdownCell(text string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\\", "\\\\", "|", "\\|", "`", "\\`", "\r\n", "<br>", "\r", "<br>", "\n", "<br>").Replace(text)
}
