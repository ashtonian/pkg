// Package client builds requests in parse's grammar from Go: filters, sort
// orders, list parameters, merge-patch members and JSON Patch operations. It
// imports only the standard library, so an SDK built on it depends on no
// server code.
//
// A catalog of typed fields per resource, usually generated, names the
// fields and fixes their operand types, so a misspelt field or an operand of
// the wrong type does not compile:
//
//	var Devices = struct {
//		Name     client.SortableText
//		Enabled  client.Field[bool]
//		LastSeen client.SortableTime
//	}{client.NewSortableText("name"), client.NewField[bool]("enabled"), client.NewSortableTime("last_seen")}
//
//	q, err := client.ListParams{
//		Filter: client.And(Devices.Name.Like("pump-%"), Devices.LastSeen.Gte(client.Ago(24*time.Hour))),
//		Sort:   client.Sort{Devices.LastSeen.Desc()},
//	}.Values()
//
// The field types follow the operators each value type can declare; the
// server still refuses an operator a field does not allow. The client
// refuses only what the grammar cannot express: an Or of nothing, Not of the
// empty filter, and a relative time finer than a second.
package client
