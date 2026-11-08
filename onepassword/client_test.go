//go:build unit

package onepassword

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ashtonian/pkg/process"
	"github.com/stretchr/testify/require"
)

type runnerFunc func(context.Context, process.Command) (process.Result, error)

func (r runnerFunc) Run(ctx context.Context, command process.Command) (process.Result, error) {
	return r(ctx, command)
}

func TestExactSelectedBatchCoverageAndWireFormats(t *testing.T) {
	metadata := `[{"id":"a","title":"first","version":3,"vault":{"id":"v"}},{"id":"b","title":"second","version":2,"vault":{"id":"v"}}]`
	first := `{"id":"a","title":"first","version":3,"vault":{"id":"v"},"fields":[{"id":"password","label":"password","value":"credential-847192","reference":"op://v/a/password"}]}`
	second := `{"id":"b","title":"second","version":2,"vault":{"id":"v"},"fields":[]}`
	for _, tc := range []struct {
		name, response string
		invalid        bool
	}{
		{"array", "[" + first + "," + second + "]", false},
		{"stream", first + "\n" + second + "\n", false},
		{"missing", "[" + first + "]", true},
		{"repeated", "[" + first + "," + first + "]", true},
		{"unexpected", "[" + first + "," + strings.Replace(second, `"id":"b"`, `"id":"another"`, 1) + "]", true},
		{"changed version", "[" + strings.Replace(first, `"version":3`, `"version":4`, 1) + "," + second + "]", true},
		{"changed vault", "[" + strings.Replace(first, `"id":"v"`, `"id":"other"`, 1) + "," + second + "]", true},
		{"extra stream", first + second + "{}", true},
		{"truncated", "[" + first + "," + second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client, err := New(WithRunner(runnerFunc(func(_ context.Context, command process.Command) (process.Result, error) {
				calls++
				if command.Args[1] == "list" {
					return process.Result{Stdout: []byte(metadata)}, nil
				}
				require.Equal(t, []string{"item", "get", "-", "--vault", "example", "--format", "json", "--reveal"}, command.Args)
				require.JSONEq(t, `[{"id":"a"},{"id":"b"}]`, string(command.Input))
				return process.Result{Stdout: []byte(tc.response)}, nil
			})))
			require.NoError(t, err)
			selected, err := client.List(t.Context(), "example")
			require.NoError(t, err)
			items, err := client.Get(t.Context(), "example", selected)
			if tc.invalid {
				require.Error(t, err)
				require.Nil(t, items)
				return
			}
			require.NoError(t, err)
			require.Len(t, items, 2)
			require.Equal(t, "credential-847192", items["a"].Fields[0].Value)
			require.Equal(t, 2, calls)
		})
	}
}

func TestBatchLimitsAccountAndFrozenEnvironment(t *testing.T) {
	policy := DefaultPolicy()
	policy.BatchSize = 2
	environment := []string{"NATIVE_TOKEN=original"}
	calls := 0
	client, err := New(WithPolicy(policy), WithAccount("account"), WithEnvironment(environment), WithRunner(runnerFunc(func(_ context.Context, command process.Command) (process.Result, error) {
		calls++
		require.Equal(t, "--account", command.Args[0])
		require.Equal(t, "account", command.Args[1])
		require.Equal(t, []string{"NATIVE_TOKEN=original"}, command.Env)
		var requested []map[string]string
		require.NoError(t, json.Unmarshal(command.Input, &requested))
		require.LessOrEqual(t, len(requested), 2)
		rows := []Item{}
		for _, item := range requested {
			rows = append(rows, Item{ItemMetadata: ItemMetadata{ID: item["id"], Title: item["id"], Vault: Vault{ID: "v"}}})
		}
		data, err := json.Marshal(rows)
		require.NoError(t, err)
		return process.Result{Stdout: data}, nil
	})))
	require.NoError(t, err)
	environment[0] = "NATIVE_TOKEN=changed"
	selected := []ItemMetadata{{ID: "a", Title: "a", Vault: Vault{ID: "v"}}, {ID: "b", Title: "b", Vault: Vault{ID: "v"}}, {ID: "c", Title: "c", Vault: Vault{ID: "v"}}}
	items, err := client.Get(t.Context(), "example", selected)
	require.NoError(t, err)
	require.Len(t, items, 3)
	require.Equal(t, 2, calls)
}

func TestReadPreservesBinaryBytesAndSanitizesNativeFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		client, err := New(WithRunner(runnerFunc(func(_ context.Context, command process.Command) (process.Result, error) {
			require.Equal(t, []string{"read", "--no-newline", "op://v/item/field"}, command.Args)
			if fail {
				return process.Result{Stderr: []byte("credential-847192")}, errors.New("native credential-847192")
			}
			return process.Result{Stdout: []byte{0, 0xff, '\n'}}, nil
		})))
		require.NoError(t, err)
		value, err := client.Read(t.Context(), "op://v/item/field")
		if fail {
			require.Error(t, err)
			require.NotContains(t, err.Error(), "credential-847192")
			require.Nil(t, value)
		} else {
			require.NoError(t, err)
			require.Equal(t, []byte{0, 0xff, '\n'}, value)
		}
	}
	client, err := New(WithRunner(runnerFunc(func(context.Context, process.Command) (process.Result, error) {
		t.Fatal("cancelled observation must not invoke the native runner")
		return process.Result{}, nil
	})))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = client.Read(ctx, "op://v/item/field")
	require.ErrorIs(t, err, context.Canceled)
}

func TestSelectedBatchesShareTheCompleteObservationByteBudget(t *testing.T) {
	policy := DefaultPolicy()
	policy.BatchSize = 1
	policy.MaxNativeBytes = 128
	calls := 0
	client, err := New(WithPolicy(policy), WithRunner(runnerFunc(func(_ context.Context, command process.Command) (process.Result, error) {
		var input []map[string]string
		require.NoError(t, json.Unmarshal(command.Input, &input))
		calls++
		item := Item{ItemMetadata: ItemMetadata{ID: input[0]["id"], Title: input[0]["id"], Vault: Vault{ID: "v"}}}
		data, err := json.Marshal(item)
		require.NoError(t, err)
		require.Less(t, len(data), policy.MaxNativeBytes)
		return process.Result{Stdout: data}, nil
	})))
	require.NoError(t, err)
	selected := []ItemMetadata{{ID: "a", Title: "a", Vault: Vault{ID: "v"}}, {ID: "b", Title: "b", Vault: Vault{ID: "v"}}}
	items, err := client.Get(t.Context(), "example-vault", selected)
	require.ErrorContains(t, err, "aggregate observation byte bound")
	require.Nil(t, items)
	require.Equal(t, 2, calls)
}
