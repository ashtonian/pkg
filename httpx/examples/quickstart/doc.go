// Command quickstart is the README Quick Start: compose HTTP compression,
// retry/backoff, and a private validator-aware cache around an ordinary
// http.Client. API_URL optionally selects the complete URL to GET twice;
// the default is httpbin's ETag endpoint. Each response is read with a limit
// and closed. Integration tests run the same program against a local server.
package main
