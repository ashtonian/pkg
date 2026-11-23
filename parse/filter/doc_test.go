//go:build unit

package filter

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The package documentation lists the field operators and the flat
// parameter suffixes; both lists are the operator table's.
func TestPackageDocListsTheOperatorTable(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "doc.go", nil, parser.ParseComments|parser.PackageClauseOnly)
	require.NoError(t, err)
	require.NotNil(t, f.Doc)
	text := strings.Join(strings.Fields(f.Doc.Text()), " ")

	var ops, suffixes []string
	for _, op := range Operators() {
		ops = append(ops, string(op))
	}
	for _, d := range operatorTable {
		if d.suffix != "" {
			suffixes = append(suffixes, d.suffix)
		}
	}
	assert.Contains(t, text, "Field operators ([Operators]): "+strings.Join(ops, ", ")+".")
	assert.Contains(t, text, "using suffix conventions: "+strings.Join(suffixes, ", ")+". No suffix means equality.")
}
