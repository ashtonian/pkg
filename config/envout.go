package config

import (
	"errors"
	"reflect"
	"strings"
)

// PrintEnv writes cfg as the environment variables that would load it under
// envPrefix — `export APP_DB_HOST='db.internal'` — one per key, named as the
// loader reads them: a list of scalars as one comma-separated value, a map
// entry by its key. A key no variable sets alone is written as a comment
// saying so: a list element of a struct, a map key the variable naming
// cannot spell, a list item holding a comma, a name another key would also
// read. A sensitive value is never exported; its line is commented out with
// no value. [WithWriter] applies; WithFormat does not.
func PrintEnv(cfg any, envPrefix string, opts ...PrintOption) error {
	if envPrefix == "" {
		return errors.New("config: PrintEnv needs an environment prefix")
	}
	po := defaultPrintOptions()
	for _, opt := range opts {
		opt(&po)
	}
	t := reflect.TypeOf(cfg)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return errors.New("config: PrintEnv needs a config")
	}
	root, err := rootSchema(t)
	if err != nil {
		return err
	}
	prefix := strings.TrimSuffix(envPrefix, "_")
	e := &envWriter{ew: &errWriter{w: po.writer}, schema: newEnvSchema(fieldsOf(t), prefix), prefix: prefix}
	e.mapping(nil, root, (&valueTree{visiting: map[visit]bool{}}).root(cfg))
	return e.ew.err
}

type envWriter struct {
	ew     *errWriter
	schema envSchema
	prefix string
}

// mapping writes the keys of struct or map node n, laid out from schema node s.
func (e *envWriter) mapping(p keyPath, s *schemaNode, n *outNode) {
	for _, entry := range n.entries {
		switch s.kind {
		case nodeBranch:
			f := s.byKey[entry.key]
			if f == nil {
				continue
			}
			e.value(p.field(entry.key), f.node, entry.node)
		case nodeMap:
			e.value(p.key(entry.key), s.elem, entry.node)
		}
	}
}

func (e *envWriter) value(p keyPath, s *schemaNode, n *outNode) {
	switch {
	case n.kind == outNull:
		e.ew.printf("# %s is null; no variable sets that\n", p)
	case n.redacted:
		if name, ok := e.name(p); ok {
			e.ew.printf("# %s is sensitive and not shown\n# export %s=\n", p, name)
			return
		}
		e.ew.printf("# %s is sensitive and not shown\n", p)
	case n.kind == outMap && (s.kind == nodeBranch || s.kind == nodeMap):
		e.mapping(p, s, n)
	case n.kind == outList && s.kind == nodeList && s.elem.kind != nodeBranch && scalarItems(n):
		items := make([]string, len(n.items))
		for i, item := range n.items {
			items[i] = scalarText(item.scalar)
			if strings.Contains(items[i], ",") {
				e.ew.printf("# %s: an item holds a comma, which a variable's list cannot; set it in a config file\n", p)
				return
			}
		}
		e.export(p, strings.Join(items, ","))
	case n.kind == outScalar:
		e.export(p, scalarText(n.scalar))
	default:
		e.ew.printf("# %s: no variable sets it; set it in a config file\n", p)
	}
}

func scalarItems(n *outNode) bool {
	for _, item := range n.items {
		if item.kind != outScalar || item.redacted {
			return false
		}
	}
	return true
}

func (e *envWriter) export(p keyPath, value string) {
	name, ok := e.name(p)
	if !ok {
		e.ew.printf("# %s: no variable sets it alone; set it in a config file\n", p)
		return
	}
	e.ew.printf("export %s=%s\n", name, shellQuote(value))
}

// name is the variable that sets key p, when the environment layer reads
// that name as p and nothing else.
func (e *envWriter) name(p keyPath) (string, bool) {
	parts := []string{e.prefix}
	for _, s := range p {
		switch s.kind {
		case segField:
			parts = append(parts, strings.ToUpper(s.name))
		case segKey:
			if !envKey(s.name) {
				return "", false
			}
			parts = append(parts, strings.ToUpper(s.name))
		default:
			return "", false
		}
	}
	name := strings.Join(parts, "_")
	readings := e.schema.readings(name)
	if len(readings) != 1 || readings[0].key != p.String() {
		return "", false
	}
	return name, true
}

// envKey reports a map key a variable name can spell: the environment layer
// reads entry keys lower-cased, from letters, digits and underscores.
func envKey(k string) bool {
	if k == "" {
		return false
	}
	for _, r := range k {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}
