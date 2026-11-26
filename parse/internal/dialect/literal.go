package dialect

import "strings"

// StringLiteral is s as an SQL string literal, its single quotes doubled.
// That is the whole escape where a backslash is an ordinary character in a
// literal: SQLite always, PostgreSQL under standard_conforming_strings =
// on, its default since 9.1. A PostgreSQL session that turns it off is not
// supported.
func StringLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// SQLiteJSONPath is the SQLite JSON path of an object member, labels its
// members' names from the document down: each label quoted, its
// backslashes and double quotes escaped, so it reads as itself whatever it
// holds. A "." or "[" in a label starts no other step, and a quoted label
// never indexes an array. Bound, the path is complete; written into a
// statement, it goes through StringLiteral.
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
