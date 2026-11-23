package core

// Key is a row's key: one value per key column, in the order the resource
// declares its key columns. A one-column key holds one value.
type Key []any
