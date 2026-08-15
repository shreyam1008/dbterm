package main

import (
	"testing"

	"github.com/shreyam1008/dbterm/internal/config"
)

func TestMergeRecoveredConnectionsIsNonDestructiveAndIdempotent(t *testing.T) {
	current := []config.ConnectionConfig{{
		ID: "user-one", Name: "Orders", Type: config.PostgreSQL, Host: "db.example.com", Port: "5432", User: "alice", Password: "new", Database: "orders", Active: true,
	}}
	recovered := []config.ConnectionConfig{
		{ID: "root-duplicate", Name: "Orders", Type: config.PostgreSQL, Host: "db.example.com", Port: "5432", User: "alice", Password: "new", Database: "orders", LastUsed: "old"},
		{ID: "user-one", Name: "Analytics", Type: config.PostgreSQL, Host: "db.example.com", Port: "5432", User: "alice", Password: "new", Database: "analytics", Active: true},
	}

	merged, added, err := mergeRecoveredConnections(current, recovered)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 || len(merged) != 2 {
		t.Fatalf("first merge added=%d len=%d, want 1 and 2", added, len(merged))
	}
	if merged[0] != current[0] {
		t.Fatal("existing user connection was changed")
	}
	if merged[1].ID == "" || merged[1].ID == "user-one" || merged[1].Active {
		t.Fatalf("recovered collision was not assigned safe state: %+v", merged[1])
	}

	again, addedAgain, err := mergeRecoveredConnections(merged, recovered)
	if err != nil {
		t.Fatal(err)
	}
	if addedAgain != 0 || len(again) != len(merged) {
		t.Fatalf("repeated merge added=%d len=%d, want 0 and %d", addedAgain, len(again), len(merged))
	}
}
