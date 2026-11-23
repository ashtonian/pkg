//go:build unit

package client_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

func ExamplePage() {
	type Device struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	answer := `{"items":[{"id":"d1","name":"pump-1"}],"page_info":{"has_next_page":true,"has_prev_page":false,"end_cursor":"c1"},"_relations":{"tags":{"d1":["outdoor"]}}}`
	var page client.Page[Device]
	err := json.Unmarshal([]byte(answer), &page)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(page.Items[0].Name, page.Relations["tags"]["d1"])

	// The next page: the same parameters with the page's end cursor.
	params := client.ListParams{Sort: client.Sort{Devices.Name.Asc()}, PageSize: 1}
	if page.PageInfo.HasNextPage {
		params.Cursor = page.PageInfo.EndCursor
	}
	q, err := params.Values()
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(q.Encode())
	// Output:
	// pump-1 [outdoor]
	// cursor=c1&page_size=1&sort=name
}

func ExampleDecodeError() {
	body := []byte(`{"code":"E2071","message":"field 'colour' is not filterable","details":{"field":"colour"}}`)
	err := fmt.Errorf("listing devices: %w", client.DecodeError(http.StatusUnprocessableEntity, body))

	refused, ok := errors.AsType[*client.Error](err)
	if ok {
		fmt.Println(refused.Status, refused.Code, refused.Details["field"])
	}
	fmt.Println(errors.Is(err, client.CodeUnknownField))
	fmt.Println(err)
	// Output:
	// 422 E2071 colour
	// true
	// listing devices: client: E2071 (HTTP 422): field 'colour' is not filterable
}
