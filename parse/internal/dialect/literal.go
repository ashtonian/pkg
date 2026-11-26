package dialect

import "strings"

// QuoteLiteral is s as a string literal that reads as s whatever the
// session sets: its single quotes doubled and, when it holds a backslash,
// an escape string (E'…') with its backslashes doubled. A plain literal
// reads a backslash as itself only under standard_conforming_strings = on,
// the default since 9.1, and an escape string under either setting. A
// string without a backslash is the plain literal, which both read alike.
func (Postgres) QuoteLiteral(s string) string {
	quoted := strings.ReplaceAll(s, "'", "''")
	if !strings.Contains(s, `\`) {
		return "'" + quoted + "'"
	}
	return "E'" + strings.ReplaceAll(quoted, `\`, `\\`) + "'"
}

// QuoteLiteral is s as a string literal, its single quotes doubled: the
// whole escape, since a backslash is an ordinary character in SQLite's
// literals.
func (SQLite) QuoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// SQLiteJSONPath is the SQLite JSON path of an object member, labels its
// members' names from the document down: each label quoted, its
// backslashes and double quotes escaped, so it reads as itself whatever it
// holds. A "." or "[" in a label starts no other step, and a quoted label
// never indexes an array. No label holds U+0000, at which SQLite ends the
// path, so `$.a` + U+0000 + `b` reads member a: patch refuses a path
// holding it where it parses one (core.CheckText), and a declared filter
// path is an identifier. Bound, the path is complete; written into a
// statement, it goes through SQLite.QuoteLiteral.
func SQLiteJSONPath(labels []string) string {
	var b strings.Builder
	b.WriteString("$")
	for _, label := range labels {
		b.WriteString(`."`)
		b.WriteString(sqliteLabelEscaper.Replace(label))
		b.WriteString(`"`)
	}
	return b.String()
}

var sqliteLabelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)
