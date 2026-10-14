package config

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// envSchema names the environment variables a config struct reads. Every
// leaf's variable is the prefix plus its key path upper-cased with "." → "_",
// so an underscore inside a key survives: APP_DB_MAX_CONNS sets db.max_conns,
// where the naive "_" → "." transform would produce db.max.conns.
//
// A map has keys no schema names, so its variables are matched by shape:
//
//   - A map of structs reads PREFIX_<MAP>_<KEY>_<FIELD>, where FIELD is one of
//     the element struct's leaves and KEY is the rest, lower-cased, so the key
//     may contain underscores too. APP_FEATURES_AUDIT_LOGS_MAX_PAGE_SIZE sets
//     features["audit_logs"].max_page_size.
//   - Any other map reads PREFIX_<MAP>_<KEY>, the whole rest lower-cased:
//     APP_LABELS_TEAM_NAME sets labels["team_name"].
//
// A variable that fits more than one key — a static field and a map entry,
// two maps whose names prefix one another, two element fields where one name
// ends the other — is ambiguous: it sets nothing, and the load fails naming
// it and every reading, rather than configuring a key the operator did not
// mean. A variable under the prefix that fits no key is not loaded.
type envSchema struct {
	prefix string // e.g. "APP_"
	static map[string][]string
	maps   []envMap
}

// envMap is one map field: the variable prefix that addresses its entries and,
// for a map of structs, the element struct's leaves.
type envMap struct {
	prefix string // e.g. "APP_FEATURES_"
	path   string // e.g. "features"
	leaves []envLeaf
}

type envLeaf struct {
	suffix string // e.g. "_MAX_PAGE_SIZE"
	path   string // e.g. "max_page_size"
}

func newEnvSchema(fields []Field, prefix string) envSchema {
	s := envSchema{prefix: prefix + "_", static: make(map[string][]string, len(fields))}
	for _, f := range fields {
		name := envName(prefix, f.Path)
		s.static[name] = append(s.static[name], f.Path)
		t := derefType(f.Type)
		if t.Kind() != reflect.Map || t.Key().Kind() != reflect.String {
			continue
		}
		m := envMap{prefix: name + "_", path: f.Path}
		if elem := derefType(t.Elem()); isBranch(elem) {
			for _, leaf := range fieldsOf(elem) {
				m.leaves = append(m.leaves, envLeaf{suffix: envName("", leaf.Path), path: leaf.Path})
			}
		}
		s.maps = append(s.maps, m)
	}
	return s
}

// envReading is one key a variable could set: its dot-path for the flat
// source, and the key in the package's grammar for messages.
type envReading struct {
	flat string
	key  string
}

// readings returns every key the variable name could set, sorted.
func (s envSchema) readings(name string) []envReading {
	var out []envReading
	for _, path := range s.static[name] {
		out = append(out, envReading{flat: path, key: path})
	}
	for _, m := range s.maps {
		rest, ok := strings.CutPrefix(name, m.prefix)
		if !ok || rest == "" {
			continue
		}
		if m.leaves == nil {
			key := strings.ToLower(rest)
			out = append(out, envReading{flat: m.path + "." + key, key: dotPath(m.path).key(key).String()})
			continue
		}
		for _, leaf := range m.leaves {
			if key, ok := strings.CutSuffix(rest, leaf.suffix); ok && key != "" {
				key = strings.ToLower(key)
				out = append(out, envReading{
					flat: m.path + "." + key + "." + leaf.path,
					key:  dotPath(m.path).key(key).String() + "." + leaf.path,
				})
			}
		}
	}
	slices.SortFunc(out, func(a, b envReading) int { return strings.Compare(a.key, b.key) })
	return slices.CompactFunc(out, func(a, b envReading) bool { return a.key == b.key })
}

// dotPath is a struct-key path, every segment a field.
func dotPath(path string) keyPath {
	var p keyPath
	for name := range strings.SplitSeq(path, ".") {
		p = p.field(name)
	}
	return p
}

// closest is the variable of a field that name most likely misspells, or "".
func (s envSchema) closest(name string) string {
	return closest(name, maps.Keys(s.static))
}

// read maps the "NAME=value" entries of environ under the prefix to their
// keys, and names the variable behind each flat key. unmatched names the
// variables under the prefix that fit no key.
func (s envSchema) read(environ []string) (values map[string]any, names map[string]string, unmatched []string, err error) {
	values = make(map[string]any)
	names = make(map[string]string)
	var ambiguous []string
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, s.prefix) {
			continue
		}
		switch readings := s.readings(name); len(readings) {
		case 0:
			unmatched = append(unmatched, name)
		case 1:
			values[readings[0].flat] = value
			names[readings[0].flat] = name
		default:
			keys := make([]string, len(readings))
			for i, r := range readings {
				keys[i] = r.key
			}
			ambiguous = append(ambiguous, name+" (could set "+strings.Join(keys, " or ")+")")
		}
	}
	if len(ambiguous) > 0 {
		return nil, nil, nil, fmt.Errorf("ambiguous environment variables: %s; rename a map key or field so only one reading fits",
			strings.Join(ambiguous, "; "))
	}
	return values, names, unmatched, nil
}

// envName is the variable name for path under prefix; an empty prefix yields
// the "_"-led suffix form used to match a map element's leaves.
func envName(prefix, path string) string {
	return prefix + "_" + strings.ToUpper(strings.ReplaceAll(path, ".", "_"))
}
