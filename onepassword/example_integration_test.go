//go:build integration

package onepassword_test

import (
	"context"
	"fmt"

	"github.com/ashtonian/pkg/onepassword"
	"github.com/ashtonian/pkg/process"
)

type fixtureRunner struct{}

func (fixtureRunner) Run(context.Context, process.Command) (process.Result, error) {
	return process.Result{Stdout: []byte(`[{"id":"item","title":"example","vault":{"id":"vault"}}]`)}, nil
}

func ExampleClient_List() {
	client, err := onepassword.New(onepassword.WithRunner(fixtureRunner{}))
	if err != nil {
		panic(err)
	}
	items, err := client.List(context.Background(), "example-vault")
	if err != nil {
		panic(err)
	}
	fmt.Println(items[0].Title)
	// Output: example
}
