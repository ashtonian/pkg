//go:build integration

package parsepgx_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/internal/corpus"
)

func TestCorpus(t *testing.T) {
	corpus.RunSQL(t, corpus.Postgres)
}
