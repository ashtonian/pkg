//go:build unit

package core_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
)

// A tag with several bad parts reports the first in the tag's order,
// every time.
func TestParseRelationTag_ReportsTheFirstBadPart(t *testing.T) {
	for range 50 {
		_, err := core.ParseRelationTag("bad junction,bad source,bad target,bad key")
		require.Error(t, err)
		assert.EqualError(t, err, `relation tag junction "bad junction" is not a SQL identifier`)
	}
	_, err := core.ParseRelationTag("widget_tags,widget_id,bad target,bad key")
	assert.EqualError(t, err, `relation tag target column "bad target" is not a SQL identifier`)
}

// A part joins a key of several columns with `+`, in key order; the source
// columns pair with the owner key by position, and a tagged field holds
// one-column target keys.
func TestParseRelationTag_CompositeKeys(t *testing.T) {
	for _, tt := range []struct {
		name    string
		tag     string
		want    *core.RelationSchema
		wantErr string
	}{
		{
			name: "one column",
			tag:  "widget_tags,widget_id,tag_id",
			want: &core.RelationSchema{JoinTable: "widget_tags", SourceColumns: []string{"widget_id"}, TargetColumns: []string{"tag_id"}, OwnerKey: []string{"id"}},
		},
		{
			name: "composite owner key",
			tag:  "stock_tags,warehouse_id+sku,tag_id,warehouse_id+sku,tenant_id",
			want: &core.RelationSchema{
				JoinTable: "stock_tags", SourceColumns: []string{"warehouse_id", "sku"}, TargetColumns: []string{"tag_id"},
				OwnerKey: []string{"warehouse_id", "sku"}, TenantColumn: "tenant_id",
			},
		},
		{name: "source without its key", tag: "stock_tags,warehouse_id+sku,tag_id", wantErr: "do not pair with primary key id: 2 and 1 columns"},
		{name: "key without its source", tag: "stock_tags,stock_id,tag_id,warehouse_id+sku", wantErr: "do not pair with primary key warehouse_id+sku: 1 and 2 columns"},
		{name: "composite target", tag: "stock_tags,stock_id,tag_id+kind", wantErr: "names several columns"},
		{name: "empty column", tag: "stock_tags,warehouse_id+,tag_id,warehouse_id+sku", wantErr: `source column "" is not a SQL identifier`},
		{name: "column twice", tag: "stock_tags,warehouse_id+warehouse_id,tag_id,warehouse_id+sku", wantErr: `source columns name "warehouse_id" twice`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := core.ParseRelationTag(tt.tag)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// Check refuses column lists that cannot pair by position.
func TestRelationSchema_Check(t *testing.T) {
	for _, tt := range []struct {
		name    string
		rel     core.RelationSchema
		wantErr string
	}{
		{name: "default owner key", rel: core.RelationSchema{JoinTable: "j", SourceColumns: []string{"s"}, TargetColumns: []string{"t"}}},
		{
			name: "composite through a target",
			rel: core.RelationSchema{
				JoinTable: "j", SourceColumns: []string{"a", "b"}, TargetColumns: []string{"x", "y"}, OwnerKey: []string{"k1", "k2"},
				TargetTable: "t", TargetKeys: []string{"x", "y"},
			},
		},
		{name: "no target column", rel: core.RelationSchema{JoinTable: "j", SourceColumns: []string{"s"}}, wantErr: "names no source or no target column"},
		{
			name:    "source longer than the owner key",
			rel:     core.RelationSchema{JoinTable: "j", SourceColumns: []string{"a", "b"}, TargetColumns: []string{"t"}},
			wantErr: "source columns (a, b) do not pair with the owner key (id)",
		},
		{
			name:    "target key shorter than the target columns",
			rel:     core.RelationSchema{JoinTable: "j", SourceColumns: []string{"s"}, TargetColumns: []string{"x", "y"}, TargetTable: "t", TargetKeys: []string{"x"}},
			wantErr: "target columns (x, y) do not pair with t's key (x)",
		},
		{name: "column twice", rel: core.RelationSchema{JoinTable: "j", SourceColumns: []string{"s"}, TargetColumns: []string{"t", "t"}}, wantErr: `target columns name "t" twice`},
		{name: "no identifier", rel: core.RelationSchema{JoinTable: "j", SourceColumns: []string{"s;"}, TargetColumns: []string{"t"}}, wantErr: "is not a SQL identifier"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.rel.Check()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}
