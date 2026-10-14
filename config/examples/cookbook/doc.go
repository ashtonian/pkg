// Package cookbook holds runnable recipes for the config package, each a
// program with a test: kubernetes (ConfigMap and Secret volumes with hot
// reload), database (settings from a table), awssecrets (AWS Secrets Manager
// through SecretsFrom), vault (koanf's Vault provider) and service (Kong,
// validation, the reloader and appliers in one main). The README's recipe
// blocks are excerpts of these files, checked by readme_test.go.
package cookbook
