package ui

import "testing"

func TestTableListSnapshotContainsOnlySelectableIdentifiers(t *testing.T) {
	snapshot := &tableListSnapshot{items: []tableListSnapshotItem{
		{label: "section"},
		{label: "public.users", identifier: "public.users"},
	}}
	if !tableListSnapshotContains(snapshot, "public.users") {
		t.Fatal("snapshot did not find its table identifier")
	}
	if tableListSnapshotContains(snapshot, "section") {
		t.Fatal("non-selectable section label was treated as a table")
	}
	if tableListSnapshotContains(snapshot, "public.orders") {
		t.Fatal("snapshot found a missing table")
	}
}
