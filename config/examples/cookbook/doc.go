// Package cookbook holds the config README's recipes as programs
// that build and pass their tests: kubernetes (ConfigMap and Secret volumes
// with hot reload), database (settings from a table), awssecrets (AWS Secrets
// Manager through SecretsFrom), vault (koanf's Vault provider) and service
// (Kong, validation, the reloader and appliers in one main). The README's
// recipe blocks are excerpts of these files, checked by readme_test.go.
package cookbook
