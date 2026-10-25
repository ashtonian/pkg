# Security

Report suspected vulnerabilities through [private vulnerability reporting](https://github.com/ashtonian/pkg/security/advisories/new). If that channel is unavailable, request a private contact without including exploit details in a public issue.

Include the module and Go versions, backend/version, a minimal synthetic reproduction, and the affected trust boundary. Do not include credentials, private tenant data or production query payloads. Supported fixes target the latest published minor version; pre-1.0 minor versions may change the API.

Parse validates and compiles requests. Applications supply authenticated tenant/policy scope, trusted schema/SQL expressions, transaction ownership and execution deadlines. Keep request size/depth/cost limits enabled. An opaque cursor is not an authorization credential. Report bypasses of these boundaries, unbounded allocation, incorrect parameterization and cross-scope query compilation privately.
