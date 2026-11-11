package config

import (
	"cmp"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

// envSchema names the environment variables a config struct reads under an
// ordered list of prefixes, most specific first. Every leaf's variable under
// a prefix is the prefix plus its key path upper-cased with "." → "_", so an
// underscore inside a key survives: APP_DB_MAX_CONNS sets db.max_conns, where
// the naive "_" → "." transform would produce db.max.conns.
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
// Each key reads its variable under the first prefix that sets one, then the
// names its env tag lists. A variable that fits more than one key — a static
// field and a map entry, two maps whose names prefix one another, two element
// fields where one name ends the other, a key under one prefix and another
// key under a second — is ambiguous: it sets nothing, and the load fails
// naming it and every reading, rather than configuring a key the operator did
// not mean. A variable under a prefix that fits no key under any prefix is
// not loaded.
type envSchema struct {
	prefixes []string              // each with its trailing "_", e.g. "APP_WORKER_", "APP_"
	named    map[string][]envNamed // every leaf's name under each prefix, and the names env tags list
	maps     []envMap
}

// envNamed is a key one variable name reads, and the reading's rank: a lower
// rank wins when several variables set one key. A name under the i-th prefix
// ranks i; the i-th name an env tag lists ranks after every prefix.
type envNamed struct {
	path string
	rank int
}

// envMap is one map field under one prefix: the variable prefix that
// addresses its entries and, for a map of structs, the element struct's
// leaves.
type envMap struct {
	prefix string // e.g. "APP_FEATURES_"
	path   string // e.g. "features"
	rank   int
	leaves []envLeaf
}

type envLeaf struct {
	suffix string // e.g. "_MAX_PAGE_SIZE"
	path   string // e.g. "max_page_size"
}

// newEnvSchema names the variables of fields under prefixes, an
// envPrefixList.
func newEnvSchema(fields []Field, prefixes []string) envSchema {
	s := envSchema{named: make(map[string][]envNamed, len(fields)*(len(prefixes)+1))}
	for _, p := range prefixes {
		s.prefixes = append(s.prefixes, p+"_")
	}
	for _, f := range fields {
		var leaves []envLeaf
		entries := envEntryMap(f.Type)
		if entries {
			if elem := derefType(derefType(f.Type).Elem()); isBranch(elem) {
				for _, leaf := range fieldsOf(elem) {
					leaves = append(leaves, envLeaf{suffix: envName("", leaf.Path), path: leaf.Path})
				}
			}
		}
		for rank, p := range prefixes {
			name := envName(p, f.Path)
			s.named[name] = append(s.named[name], envNamed{path: f.Path, rank: rank})
			if entries {
				s.maps = append(s.maps, envMap{prefix: name + "_", path: f.Path, rank: rank, leaves: leaves})
			}
		}
		for i, tagName := range envTagNames(f.Tag) {
			s.named[tagName] = append(s.named[tagName], envNamed{path: f.Path, rank: len(prefixes) + i})
		}
	}
	return s
}

// envEntryMap reports a map whose entries variables address one by one: a
// map keyed by strings, which a variable name can spell.
func envEntryMap(t reflect.Type) bool {
	t = derefType(t)
	return t.Kind() == reflect.Map && t.Key().Kind() == reflect.String
}

// envPrefixList is prefixes as the environment layer reads them: in order,
// each without its trailing "_", with empty and repeated prefixes left out.
func envPrefixList(prefixes []string) []string {
	var out []string
	for _, p := range prefixes {
		p = strings.TrimSuffix(p, "_")
		if p != "" && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

// envReading is one key a variable could set: its dot-path for the flat
// source, and the key in the package's grammar for messages.
type envReading struct {
	flat string
	key  string
	rank int // see envNamed
}

// readings returns every key the variable name could set, sorted.
func (s envSchema) readings(name string) []envReading {
	var out []envReading
	for _, n := range s.named[name] {
		out = append(out, envReading{flat: n.path, key: n.path, rank: n.rank})
	}
	for _, m := range s.maps {
		rest, ok := strings.CutPrefix(name, m.prefix)
		if !ok || rest == "" {
			continue
		}
		if m.leaves == nil {
			key := strings.ToLower(rest)
			out = append(out, envReading{flat: m.path + "." + key, key: dotPath(m.path).key(key).String(), rank: m.rank})
			continue
		}
		for _, leaf := range m.leaves {
			if key, ok := strings.CutSuffix(rest, leaf.suffix); ok && key != "" {
				key = strings.ToLower(key)
				out = append(out, envReading{
					flat: m.path + "." + key + "." + leaf.path,
					key:  dotPath(m.path).key(key).String() + "." + leaf.path,
					rank: m.rank,
				})
			}
		}
	}
	slices.SortFunc(out, func(a, b envReading) int {
		return cmp.Or(strings.Compare(a.key, b.key), cmp.Compare(a.rank, b.rank))
	})
	return slices.CompactFunc(out, func(a, b envReading) bool { return a.key == b.key })
}

// prefix is the prefix a reading of the given rank is under, without its
// trailing "_", or "" for a name an env tag lists.
func (s envSchema) prefix(rank int) string {
	if rank >= len(s.prefixes) {
		return ""
	}
	return strings.TrimSuffix(s.prefixes[rank], "_")
}

// underPrefix reports a variable named under one of the prefixes.
func (s envSchema) underPrefix(name string) bool {
	return slices.ContainsFunc(s.prefixes, func(p string) bool { return strings.HasPrefix(name, p) })
}

// pattern names the layer's variables for messages: "APP_WORKER_*, APP_*".
func (s envSchema) pattern() string {
	return strings.Join(s.prefixes, "*, ") + "*"
}

// describe is a reading as an ambiguity message names it. With more than
// one prefix it says which prefix, or the env tag, the reading comes from.
func (s envSchema) describe(r envReading) string {
	switch {
	case len(s.prefixes) < 2:
		return r.key
	case r.rank < len(s.prefixes):
		return r.key + " under " + s.prefix(r.rank)
	default:
		return r.key + " by its env tag"
	}
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
	return closest(name, maps.Keys(s.named))
}

// envVariable is the variable a value of the environment layer came from,
// and the prefix it was read under: "" for a name an env tag lists.
type envVariable struct {
	name   string
	prefix string
}

// read maps the "NAME=value" entries of environ under the prefixes, and
// those an env tag names, to their keys, and names the variable behind each
// flat key: where several set one key, the one of the lowest rank — under
// the most specific prefix, else the one its tag lists first. unmatched
// names the variables under a prefix that fit no key under any.
func (s envSchema) read(environ []string) (values map[string]any, from map[string]envVariable, unmatched []string, err error) {
	values = make(map[string]any)
	from = make(map[string]envVariable)
	ranks := make(map[string]int)
	var ambiguous []string
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || !s.underPrefix(name) && len(s.named[name]) == 0 {
			continue
		}
		switch readings := s.readings(name); len(readings) {
		case 0:
			unmatched = append(unmatched, name)
		case 1:
			r := readings[0]
			if rank, set := ranks[r.flat]; set && rank <= r.rank {
				continue
			}
			values[r.flat], ranks[r.flat] = value, r.rank
			from[r.flat] = envVariable{name: name, prefix: s.prefix(r.rank)}
		default:
			keys := make([]string, len(readings))
			for i, r := range readings {
				keys[i] = s.describe(r)
			}
			ambiguous = append(ambiguous, name+" (could set "+strings.Join(keys, " or ")+")")
		}
	}
	if len(ambiguous) > 0 {
		return nil, nil, nil, fmt.Errorf("ambiguous environment variables: %s; rename a map key or field so only one reading fits",
			strings.Join(ambiguous, "; "))
	}
	// A whole map or format:"json" section and a key beneath it cannot both
	// be set, whichever prefixes set them: no merge of the two is the one an
	// operator obviously meant.
	for _, key := range slices.Sorted(maps.Keys(values)) {
		segs := strings.Split(key, ".")
		for i := 1; i < len(segs); i++ {
			parent := strings.Join(segs[:i], ".")
			if _, ok := values[parent]; ok {
				return nil, nil, nil, fmt.Errorf("environment variables %s and %s set both a value and keys beneath it", from[parent].name, from[key].name)
			}
		}
	}
	return values, from, unmatched, nil
}

// envNames are the variables that address one position of a config schema
// as diagnostics walk it: one name under each prefix, most specific first.
// They follow the rule the loader reads by — a key's segments upper-cased
// and joined with "_" after the prefix — and stop where the loader reads no
// variable: beneath a list element, and beneath a map entry, where a map's
// entries are not addressed one by one.
type envNames struct {
	names   []string // nil where no variable reaches
	inEntry bool
}

// rootEnvNames are the names at the root of a schema under prefixes, an
// envPrefixList.
func rootEnvNames(prefixes []string) envNames {
	return envNames{names: slices.Clone(prefixes)}
}

// field is the names of the key segment key beneath e.
func (e envNames) field(key string) envNames {
	return e.extend(envName("", key))
}

// entries is the names of the entries of a map of type t at e: a <KEY>
// placeholder for the entry's key.
func (e envNames) entries(t reflect.Type) envNames {
	if e.inEntry || !envEntryMap(t) {
		return envNames{inEntry: true}
	}
	next := e.extend("_<KEY>")
	next.inEntry = true
	return next
}

func (e envNames) extend(suffix string) envNames {
	if len(e.names) == 0 {
		return envNames{inEntry: e.inEntry}
	}
	out := envNames{names: make([]string, len(e.names)), inEntry: e.inEntry}
	for i, n := range e.names {
		out.names[i] = n + suffix
	}
	return out
}

// set lists the variables that set a key of type t at e whose struct tag is
// tag, in precedence order: its name under each prefix, then the names its
// env tag lists. A value no variable sets whole — a section, a list of
// sections, a map a string cannot fill — has none.
func (e envNames) set(tag reflect.StructTag, t reflect.Type) []string {
	if len(e.names) == 0 || !variableSetsField(tag, t) {
		return nil
	}
	return append(slices.Clone(e.names), envTagNames(tag)...)
}

// envTagNames returns the variables a field's env tag lists, in order.
func envTagNames(tag reflect.StructTag) []string {
	v, ok := tag.Lookup("env")
	if !ok {
		return nil
	}
	return strings.Split(v, ",")
}

// envVarName matches what a POSIX shell accepts as a variable's name.
var envVarName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// envName is the variable name for path under prefix; an empty prefix yields
// the "_"-led suffix form used to match a map element's leaves.
func envName(prefix, path string) string {
	return prefix + "_" + strings.ToUpper(strings.ReplaceAll(path, ".", "_"))
}
