package ui

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/shreyam1008/dbterm/internal/config"
)

func TestLoadSQLiteForeignKeyReferences(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`PRAGMA foreign_keys=ON;
CREATE TABLE users (id INTEGER PRIMARY KEY);
CREATE TABLE orders (
  id INTEGER PRIMARY KEY,
  user_id INTEGER,
  FOREIGN KEY (user_id) REFERENCES users(id)
);`); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}

	refs, err := loadForeignKeyReferences(context.Background(), db, config.SQLite, "orders", "")
	if err != nil {
		t.Fatalf("load foreign keys: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("foreign key count = %d, want 1", len(refs))
	}
	ref := refs[0]
	if len(ref.columns) != 1 || ref.columns[0].localColumn != "user_id" || ref.targetTable != "users" || ref.columns[0].targetColumn != "id" {
		t.Fatalf("foreign key = %#v, want user_id -> users(id)", ref)
	}

	matches := foreignKeysForColumn(refs, "USER_ID")
	if len(matches) != 1 {
		t.Fatalf("case-insensitive matches = %d, want 1", len(matches))
	}
}

func TestForeignKeysForColumnReturnsNoUnrelatedConstraints(t *testing.T) {
	refs := []foreignKeyReference{
		{columns: []foreignKeyColumnReference{{localColumn: "account_id"}}},
		{columns: []foreignKeyColumnReference{{localColumn: "owner_id"}}},
	}
	if matches := foreignKeysForColumn(refs, "missing_id"); len(matches) != 0 {
		t.Fatalf("matches = %#v, want none", matches)
	}
}

func TestForeignKeyMatchingPreservesCaseDistinctPostgresColumns(t *testing.T) {
	refs := []foreignKeyReference{
		{name: "quoted", columns: []foreignKeyColumnReference{{localColumn: "Foo"}}},
		{name: "plain", columns: []foreignKeyColumnReference{{localColumn: "foo"}}},
	}
	if matches := foreignKeysForColumn(refs, "Foo"); len(matches) != 1 || matches[0].name != "quoted" {
		t.Fatalf("exact quoted match = %#v, want only quoted FK", matches)
	}
	if matches := foreignKeysForColumn(refs, "FOO"); len(matches) != 0 {
		t.Fatalf("ambiguous folded match = %#v, want none", matches)
	}

	values := map[string]foreignKeyRowValue{
		"Foo": {value: int64(7)},
		"foo": {value: int64(9)},
	}
	if value, ok := foreignKeyRowValueForColumn(values, "Foo"); !ok || value.value != int64(7) {
		t.Fatalf("exact row value = %#v,%v, want 7,true", value, ok)
	}
	if _, ok := foreignKeyRowValueForColumn(values, "FOO"); ok {
		t.Fatal("ambiguous case-folded row value unexpectedly matched")
	}
}

func TestCompositeForeignKeyStaysGroupedAndUsesEveryTargetColumn(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`PRAGMA foreign_keys=ON;
CREATE TABLE users (
  tenant_id INTEGER,
  id INTEGER,
  PRIMARY KEY (tenant_id, id)
);
CREATE TABLE orders (
  id INTEGER PRIMARY KEY,
  tenant_id INTEGER,
  user_id INTEGER,
  FOREIGN KEY (tenant_id, user_id) REFERENCES users(tenant_id, id)
);`); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}

	refs, err := loadForeignKeyReferences(context.Background(), db, config.SQLite, "orders", "")
	if err != nil {
		t.Fatalf("load foreign keys: %v", err)
	}
	if len(refs) != 1 || len(refs[0].columns) != 2 {
		t.Fatalf("composite foreign key = %#v, want one two-column constraint", refs)
	}
	if matches := foreignKeysForColumn(refs, "user_id"); len(matches) != 1 || len(matches[0].columns) != 2 {
		t.Fatalf("selected component matches = %#v, want the complete constraint", matches)
	}

	predicates, err := foreignKeyTargetPredicates(refs[0], map[string]foreignKeyRowValue{
		"tenant_id": {value: int64(7)},
		"user_id":   {value: int64(42)},
	})
	if err != nil {
		t.Fatalf("build target predicates: %v", err)
	}
	if len(predicates) != 2 || predicates[0].column != "tenant_id" || predicates[0].value != int64(7) || predicates[1].column != "id" || predicates[1].value != int64(42) {
		t.Fatalf("target predicates = %#v", predicates)
	}
}

func TestCompositeForeignKeyWithNullDoesNotNavigate(t *testing.T) {
	ref := foreignKeyReference{
		name:        "orders_user_fk",
		targetTable: "users",
		columns: []foreignKeyColumnReference{
			{localColumn: "tenant_id", targetColumn: "tenant_id", ordinal: 1},
			{localColumn: "user_id", targetColumn: "id", ordinal: 2},
		},
	}
	_, err := foreignKeyTargetPredicates(ref, map[string]foreignKeyRowValue{
		"tenant_id": {value: int64(7)},
		"user_id":   {isNull: true},
	})
	if !errors.Is(err, errForeignKeyValueIsNull) {
		t.Fatalf("error = %v, want NULL relationship error", err)
	}
}

func TestLoadSQLiteIncomingForeignKeyReferences(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`PRAGMA foreign_keys=ON;
CREATE TABLE people (
  tenant_id INTEGER,
  id INTEGER,
  PRIMARY KEY (tenant_id, id)
);
CREATE TABLE visits (
  id INTEGER PRIMARY KEY,
  tenant_id INTEGER,
  person_id INTEGER,
  FOREIGN KEY (tenant_id, person_id) REFERENCES people(tenant_id, id)
);
CREATE TABLE payments (
  id INTEGER PRIMARY KEY,
  visit_id INTEGER REFERENCES visits(id)
);`); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}

	refs, err := loadIncomingForeignKeyReferences(
		context.Background(), db, config.SQLite, "people", "", []string{"payments", "people", "visits"},
	)
	if err != nil {
		t.Fatalf("load incoming foreign keys: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("incoming foreign key count = %d, want 1: %#v", len(refs), refs)
	}
	ref := refs[0]
	if ref.sourceTable != "visits" || ref.targetTable != "people" || len(ref.columns) != 2 {
		t.Fatalf("incoming foreign key = %#v, want visits -> people with two columns", ref)
	}
	if ref.columns[0].localColumn != "tenant_id" || ref.columns[0].targetColumn != "tenant_id" ||
		ref.columns[1].localColumn != "person_id" || ref.columns[1].targetColumn != "id" {
		t.Fatalf("incoming composite mapping = %#v", ref.columns)
	}
}

func TestRelationshipsForColumnIncludesBothDirections(t *testing.T) {
	outgoing := []foreignKeyReference{{
		name: "visits_room_fk", targetTable: "rooms",
		columns: []foreignKeyColumnReference{{localColumn: "room_id", targetColumn: "id"}},
	}}
	incoming := []foreignKeyReference{{
		name: "payments_visit_fk", sourceTable: "payments", targetTable: "visits",
		columns: []foreignKeyColumnReference{{localColumn: "visit_id", targetColumn: "id"}},
	}}

	roomRelationships := relationshipsForColumn("visits", "room_id", outgoing, incoming)
	if len(roomRelationships) != 1 || roomRelationships[0].direction != relationshipOutgoing || roomRelationships[0].targetTable != "rooms" {
		t.Fatalf("room relationships = %#v, want outgoing rooms", roomRelationships)
	}
	visitRelationships := relationshipsForColumn("visits", "id", outgoing, incoming)
	if len(visitRelationships) != 1 || visitRelationships[0].direction != relationshipIncoming || visitRelationships[0].sourceTable != "payments" {
		t.Fatalf("visit relationships = %#v, want incoming payments", visitRelationships)
	}
}

func TestIncomingForeignKeyPredicatesUseParentTargetColumns(t *testing.T) {
	ref := foreignKeyReference{
		name: "visits_person_fk", sourceTable: "visits", targetTable: "people",
		columns: []foreignKeyColumnReference{
			{localColumn: "tenant_id", targetColumn: "tenant_id", ordinal: 0},
			{localColumn: "person_id", targetColumn: "id", ordinal: 1},
		},
	}
	predicates, err := incomingForeignKeyPredicates(ref, map[string]foreignKeyRowValue{
		"tenant_id": {value: int64(7)},
		"id":        {value: int64(42)},
	})
	if err != nil {
		t.Fatalf("build incoming predicates: %v", err)
	}
	if len(predicates) != 2 || predicates[0].column != "tenant_id" || predicates[0].value != int64(7) ||
		predicates[1].column != "person_id" || predicates[1].value != int64(42) {
		t.Fatalf("incoming predicates = %#v", predicates)
	}
}

func TestSQLiteSameValueSearchFindsOnlyExactMatchesInSameNamedColumns(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`CREATE TABLE people (person_code TEXT PRIMARY KEY, name TEXT);
CREATE TABLE visits (id INTEGER PRIMARY KEY, person_code TEXT);
CREATE TABLE notes (id INTEGER PRIMARY KEY, person_code TEXT);
INSERT INTO people VALUES ('P-42', 'Ada');
INSERT INTO visits VALUES (1, 'P-42');
INSERT INTO notes VALUES (1, 'P-99');`); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}

	locations, err := loadSameNamedColumnLocations(context.Background(), db, config.SQLite, "person_code", []string{"people", "visits", "notes"})
	if err != nil {
		t.Fatalf("load column locations: %v", err)
	}
	matches, err := filterSameValueMatches(context.Background(), db, config.SQLite, locations, foreignKeyRowValue{value: "P-42"})
	if err != nil {
		t.Fatalf("filter same-value matches: %v", err)
	}
	if len(matches) != 2 || matches[0].table != "people" || matches[1].table != "visits" {
		t.Fatalf("same-value matches = %#v, want people and visits", matches)
	}
}
