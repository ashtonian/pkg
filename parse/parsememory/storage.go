package parsememory

import "github.com/ashtonian/pkg/parse/core"

// storage is how the store holds column values, which a row scan reads
// back (backend.Storage): each value in the one storage form a row holds,
// core.StorageValue, which SQLite binds too, so a filter selects the same
// rows of the same writes on either. A row hands a timestamp over as its
// time.Time (rows), so only documents and arrays come back as text.
type storage struct{}

func (storage) Name() string { return Name }

func (storage) StoresAsText(t core.FieldType) bool {
	return core.IsDocument(t) || t == core.FieldTypeArray
}
