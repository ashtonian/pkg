module github.com/ashtonian/pkg/parse/examples/server

go 1.27.0

require (
	github.com/alecthomas/kong v1.17.0
	github.com/ashtonian/pkg/config v0.14.0
	github.com/ashtonian/pkg/config/kongprovider v0.6.0
	github.com/ashtonian/pkg/parse v0.19.0 // x-release-please-version
	github.com/ashtonian/pkg/parse/parsememory v0.19.0 // x-release-please-version
	github.com/ashtonian/pkg/parse/parsepgx v0.19.0 // x-release-please-version
	github.com/ashtonian/pkg/parse/parsesql v0.19.0 // x-release-please-version
	github.com/jackc/pgx/v5 v5.13.0
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/ashtonian/pkg/gowalker v0.10.0 // indirect
	github.com/ashtonian/pkg/jsonvalue v0.1.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/evanphx/json-patch/v5 v5.9.11 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/gabriel-vasile/mimetype v1.4.16 // indirect
	github.com/go-playground/locales v0.14.2 // indirect
	github.com/go-playground/universal-translator v0.18.2 // indirect
	github.com/go-playground/validator/v10 v10.30.6 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.3 // indirect
	github.com/knadh/koanf/maps v0.1.3 // indirect
	github.com/knadh/koanf/parsers/yaml v1.1.1 // indirect
	github.com/knadh/koanf/providers/confmap v1.0.1 // indirect
	github.com/knadh/koanf/v2 v2.4.1 // indirect
	github.com/leodido/go-urn v1.5.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mitchellh/copystructure v1.2.0 // indirect
	github.com/mitchellh/reflectwalk v1.0.2 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/tidwall/btree v1.8.2 // indirect
	github.com/vmihailenco/msgpack/v5 v5.4.1 // indirect
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	go.yaml.in/yaml/v3 v3.0.6 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	modernc.org/libc v1.80.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	modernc.org/sqlite v1.62.0 // indirect
)

// The example runs against the Parse working tree it sits in.
replace (
	github.com/ashtonian/pkg/parse => ../..
	github.com/ashtonian/pkg/parse/parsememory => ../../parsememory
	github.com/ashtonian/pkg/parse/parsepgx => ../../parsepgx
	github.com/ashtonian/pkg/parse/parsesql => ../../parsesql
)
