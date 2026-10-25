//go:build unit

package client_test

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ashtonian/pkg/parse/client"
)

// A generated SDK declares one catalog per resource; this is a hand-written
// one for a device resource.
var Devices = struct {
	Name     client.SortableText
	Enabled  client.Field[bool]
	LastSeen client.SortableTime
	Tags     client.Relation[string]
}{
	Name:     client.NewSortableText("name"),
	Enabled:  client.NewField[bool]("enabled"),
	LastSeen: client.NewSortableTime("last_seen"),
	Tags:     client.NewRelation[string]("tags"),
}

func Example() {
	params := client.ListParams{
		Filter: client.And(
			Devices.Name.Like("pump-%"),
			Devices.Enabled.Eq(true),
			Devices.LastSeen.Gte(client.Ago(24*time.Hour)),
		),
		Sort:     client.Sort{Devices.LastSeen.Desc(), Devices.Name.Asc()},
		PageSize: 50,
	}
	q, err := params.Values()
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(q.Get("filter"))
	fmt.Println(q.Get("sort"))
	// Output:
	// {"$and":[{"name":{"$like":"pump-%"}},{"enabled":{"$eq":true}},{"last_seen":{"$gte":"now-1d"}}]}
	// -last_seen,name
}

func ExampleNullable() {
	type DevicePatch struct {
		Name client.Nullable[string] `json:"name,omitzero"`
		Note client.Nullable[string] `json:"note,omitzero"`
		Rack client.Nullable[int]    `json:"rack,omitzero"`
	}
	body, err := json.Marshal(DevicePatch{Name: client.Set("pump-7"), Note: client.Null[string]()})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(string(body))
	// Output: {"name":"pump-7","note":null}
}

func ExampleRelation_Add() {
	body, err := json.Marshal([]client.Operation{
		Devices.Tags.Add("outdoor"),
		Devices.Tags.Remove("indoor"),
		client.Replace(client.Pointer("name"), "pump-7"),
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(string(body))
	// Output: [{"op":"add","path":"/tags","value":["outdoor"]},{"op":"remove","path":"/tags","value":["indoor"]},{"op":"replace","path":"/name","value":"pump-7"}]
}
