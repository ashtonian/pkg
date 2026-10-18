# Gowalker documentation

Start with the job your library needs to do: read a struct's annotations, map
tagged names to fields, or reuse those fields to read and write values.

| Guide | Start here when you want to… |
|---|---|
| [Quick start](../README.md) | Discover fields and see exactly what their tags return |
| [Usage](usage.md) | Generate help from annotations, read tagged columns, or define custom field rules |
| [API reference](api.md) | Check a method's inputs, results, errors or ownership rules |
| [How it works](architecture.md) | Understand what is compiled and reused |
| [Scope](scope.md) | Decide which behavior belongs in your library |
| [Performance](performance.md) | Avoid repeated work and understand allocation costs |
| [Executable examples](../example_test.go) | See examples tested with the implementation |

Create one engine for your library's field rules, compile each type you use, and
reuse the resulting field descriptions and readers. Tags describe what a field
means to your library; gowalker handles discovery and access. It can also walk
types and values when you need to inspect a whole graph.
