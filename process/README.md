# process

`process` supervises bounded one-shot native commands on Linux and macOS. It owns command deadlines, separate output limits and cleanup of the process group; callers own native effects, admission and remote reconciliation.

## Quick start

Requires Go 1.27 or later.

```go
package main

import (
    "context"
    "fmt"

    "github.com/ashtonian/pkg/process"
)

func main() {
    supervisor, err := process.New()
    if err != nil { panic(err) }
    result, err := supervisor.Run(context.Background(), process.Command{
        Name: "printf", Args: []string{"ready"},
    })
    if err != nil { panic(err) }
    fmt.Println(string(result.Stdout))
}
```

```text
ready
```

## Configuration

| Option | Type | Default | Contract |
|---|---|---|---|
| `WithTimeout` | duration | `30s` | Positive whole-command deadline |
| `WithGrace` | duration | `2s` | Positive TERM/pipe cleanup interval before group kill |
| `WithMaxOutputBytes` | int | `1048576` | Positive independent stdout/stderr cap through 64 MiB |

| Command field | Type | Default | Contract |
|---|---|---|---|
| `Name` | string | required | Native executable; no implicit shell |
| `Args` | []string | nil | Exact native arguments |
| `Input` | []byte | nil | Owned stdin content |
| `Dir` | string | inherited | Native working directory |
| `Env` | []string | inherited when nil | Complete native environment when supplied |
| `ExtraFiles` | []*os.File | nil | Caller-owned inherited file descriptors; caller closes them |

## API and external dependencies

`New(options...)` validates execution policy. `Supervisor.Run(ctx, Command)` waits for the direct process, bounds pipe cleanup, kills remaining members of its process group and returns `Result{PID, Stdout, Stderr}`. Errors retain native `*exec.ExitError`, context cancellation/deadlines and `ErrOutputLimit`. `ErrUnsupportedPlatform` rejects execution where group supervision is unavailable. Runtime uses only the standard library plus the selected executable. Tests require standard Linux/macOS process tools; Testify is a test dependency.

## Build and test

`GOWORK=off go test -tags=unit,integration -race -count=1 ./...` exercises real child processes, descendant cleanup, inherited descriptors, output overflow and nonzero exits. Repository targets `make test-native-unit`, `make test-native-integration` and `make native-release-check` cover both native modules and fresh archive consumers.

## Operations

Pass the application lifecycle context into each call; the supervisor creates no background service. A timeout or output overflow can occur after a native side effect. Retain its admission evidence and reconcile that effect instead of automatically retrying it. Inspect error identities and bounded output; the package has no automatic logs, metrics or retry loop.

Commands must remain in their owned process group. Detached processes and remote work are outside this ownership boundary and need an appropriate native lifecycle protocol.

## Security

Arguments and output may contain credentials. The package never logs streams automatically, but callers must sanitize errors and output before publishing them. Use executable/argument vectors and avoid shells for untrusted data. Local process cleanup is not authorization or a distributed execution fence.

## Code map

| File | Responsibility |
|---|---|
| `run.go` | Validated policy, native execution and bounded stream capture |
| `group_unix.go` | Linux/macOS group creation, TERM and KILL cleanup |
| `group_other.go` | Fail-closed unsupported-platform boundary |
| `run_integration_test.go` | Native process lifecycle regressions |

[MIT license](LICENSE).
