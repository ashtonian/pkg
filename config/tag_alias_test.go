package config

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type aliasCfg struct {
	DB struct {
		MaxConns int `koanf:"max_conns" alias:"maxconns,max_connections"`
	} `koanf:"db" alias:"database"`
	Items []struct {
		Port int `koanf:"port" alias:"listen"`
	} `koanf:"items"`
}

// A key an alias names sets its field, in any section and in list
// elements, and the load warns that it should be renamed.
func TestAlias_ReadsFormerKeysAndWarns(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, file, "database:\n  Max_Connections: 50\nitems:\n  - listen: 80\n")
	var logs bytes.Buffer
	got, err := Load[aliasCfg](t.Context(), WithFile(file), WithSkipValidation(),
		WithLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))))
	require.NoError(t, err)
	assert.Equal(t, 50, got.DB.MaxConns)
	require.Len(t, got.Items, 1)
	assert.Equal(t, 80, got.Items[0].Port)
	assert.Contains(t, logs.String(), "set under a former name")
	assert.Contains(t, logs.String(), "database (now db) in config file "+file)
	assert.Contains(t, logs.String(), "db.Max_Connections (now db.max_conns)")
	assert.Contains(t, logs.String(), "items[0].listen (now items[0].port)")
}

// Sources merge after the rewrite, so the current key in a later source
// wins over an alias in an earlier one; one source setting both fails.
func TestAlias_PrecedenceAndConflicts(t *testing.T) {
	dir := t.TempDir()
	old, current := filepath.Join(dir, "old.yaml"), filepath.Join(dir, "new.yaml")
	writeFile(t, old, "db:\n  maxconns: 10\n")
	writeFile(t, current, "db:\n  max_conns: 20\n")
	got, err := Load[aliasCfg](t.Context(), WithFiles(old, current), WithSkipValidation())
	require.NoError(t, err)
	assert.Equal(t, 20, got.DB.MaxConns)

	both := filepath.Join(dir, "both.yaml")
	writeFile(t, both, "db:\n  maxconns: 10\n  max_conns: 20\n")
	_, err = Load[aliasCfg](t.Context(), WithFile(both), WithSkipValidation())
	require.ErrorIs(t, err, ErrValidation)
	assert.Contains(t, err.Error(), "keys spelled two ways set one field: db.max_conns")
}

// An alias the key grammar cannot hold, or one that collides with a key or
// another alias, is refused before anything loads.
func TestAlias_Refused(t *testing.T) {
	type collides struct {
		A int `koanf:"a" alias:"b"`
		B int `koanf:"b"`
	}
	_, err := Load[collides](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "which field A names as an alias")

	type shared struct {
		A int `koanf:"a" alias:"old"`
		B int `koanf:"b" alias:"old"`
	}
	_, err = Load[shared](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), `both have alias "old"`)

	type Inner struct {
		X int `koanf:"x"`
	}
	type squashed struct {
		Inner `koanf:",squash" alias:"old"`
	}
	_, err = Load[squashed](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), "is squashed")

	type bad struct {
		A int `koanf:"a" alias:"x.y"`
	}
	_, err = Load[bad](t.Context())
	require.ErrorIs(t, err, ErrSchema)
	assert.Contains(t, err.Error(), `alias "x.y"`)
}

// The JSON Schema accepts an alias, marked deprecated, so an editor does not
// reject a file the loader reads.
func TestAlias_JSONSchemaListsDeprecatedNames(t *testing.T) {
	raw, err := JSONSchema[aliasCfg]()
	require.NoError(t, err)
	var doc struct {
		Properties map[string]struct {
			Deprecated  bool   `json:"deprecated"`
			Description string `json:"description"`
			Properties  map[string]struct {
				Deprecated bool `json:"deprecated"`
			} `json:"properties"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	assert.False(t, doc.Properties["db"].Deprecated)
	assert.True(t, doc.Properties["database"].Deprecated)
	assert.Equal(t, "Renamed to db.", doc.Properties["database"].Description)
	assert.True(t, doc.Properties["db"].Properties["maxconns"].Deprecated)
	assert.True(t, doc.Properties["db"].Properties["max_connections"].Deprecated)
}
