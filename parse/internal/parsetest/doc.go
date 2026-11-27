// Package parsetest holds what parse's package tests (the root package's
// and filter's) and its database corpus (internal/corpus) share: the model
// types, the schemas and resources built on them and the calls that run
// them. A fixture lives here when a package's tests and the corpus both use
// it; fixtures only one of them uses stay with it.
package parsetest
