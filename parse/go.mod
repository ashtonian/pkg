module github.com/ashtonian/pkg/parse

go 1.27.0

// SDK development dependencies may contain Go sources at any depth.
ignore node_modules

require (
	github.com/ashtonian/pkg/gowalker v0.10.0
	github.com/ashtonian/pkg/jsonvalue v0.1.0
	github.com/evanphx/json-patch/v5 v5.9.11
	github.com/google/uuid v1.6.0
	github.com/shopspring/decimal v1.6.0
	github.com/stretchr/testify v1.12.1
	github.com/vmihailenco/msgpack/v5 v5.4.1
)

require (
	github.com/vmihailenco/tagparser/v2 v2.0.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
)
