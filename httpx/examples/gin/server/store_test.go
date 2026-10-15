//go:build unit

package main

import (
	"encoding/json"
	"testing"
)

func TestUserStoreSnapshotsAndStableETags(t *testing.T) {
	store := NewUserStore()
	first, etag := store.List()
	before, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for range 100 {
		users, gotETag := store.List()
		body, err := json.Marshal(users)
		if err != nil {
			t.Fatal(err)
		}
		if gotETag != etag || string(body) != string(before) {
			t.Fatal("unchanged collection changed its body or ETag")
		}
	}
	read, _, _ := store.Get(1)
	updated, _, _ := store.Update(1, "changed", "new@example.com")
	if read.Name != "Alice" || first[0].Name != "Alice" {
		t.Fatal("update mutated an earlier snapshot after releasing the store lock")
	}
	updated.Name = "caller mutation"
	read, _, _ = store.Get(1)
	if read.Name != "changed" {
		t.Fatal("caller mutated stored user through the returned pointer")
	}
	_, newETag := store.List()
	if etag == newETag {
		t.Fatal("mutation did not change the collection ETag")
	}
}

func TestDeviceStoreSnapshotsAndStableETags(t *testing.T) {
	store := NewDeviceStore()
	first, etag := store.List()
	before, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for range 100 {
		devices, gotETag := store.List()
		body, err := json.Marshal(devices)
		if err != nil {
			t.Fatal(err)
		}
		if gotETag != etag || string(body) != string(before) {
			t.Fatal("unchanged collection changed its body or ETag")
		}
	}
	first[0].Name = "caller mutation"
	device, _, _ := store.Get(1)
	if device.Name != "Temp Sensor A" {
		t.Fatal("caller mutated stored device through a collection snapshot")
	}
	device.Name = "another mutation"
	device, _, _ = store.Get(1)
	if device.Name != "Temp Sensor A" {
		t.Fatal("caller mutated stored device through a read snapshot")
	}
}
