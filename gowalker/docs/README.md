# Gowalker documentation

Gowalker is for Go libraries that users configure by tagging their own structs.
Start with the job your library needs to do.

| Guide | Start here when you want to… |
|---|---|
| [Quick start](../README.md) | Decide whether gowalker fits, pick a naming convention, read your first tags |
| [Recipes](usage.md) | Cache per-type metadata, follow `json`/`koanf` naming, nest sections, inherit section tags, handle nil and errors |
| [API reference](api.md) | Check a method's inputs, results, errors or ownership rules |
| [How it works](architecture.md) | Understand what is compiled and reused |
| [Scope](scope.md) | Decide which behavior belongs in your library |
| [Performance](performance.md) | Avoid repeated work and understand allocation costs |
| [Executable examples](../example_test.go) | See examples tested with the implementation |

Use `gowalker.Compile[T]()` for Go field names. For your own tag convention,
keep one engine in a package variable, put what you derive per type in a
`TypeCache`, and reuse accessors for fields you read repeatedly. Tags describe
what a field means to your library; gowalker handles discovery and access.
