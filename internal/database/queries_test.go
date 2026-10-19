package database

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/shreyam1008/dbterm/internal/config"
)

func TestSQLiteTableDiscoveryKeepsUserNamesBeginningWithSQLite(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE sqliteledger(id INTEGER PRIMARY KEY AUTOINCREMENT); CREATE TABLE normal(id INTEGER);`); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(ListTablesQuery(config.SQLite))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names, ","); got != "normal,sqliteledger" {
		t.Fatalf("discovered tables = %q, want normal,sqliteledger without sqlite_sequence", got)
	}
}

func TestCheckpointJournalNamespaceIsExact(t *testing.T) {
	for _, test := range []struct {
		engine config.DBType
		name   string
		owned  bool
	}{
		{config.SQLite, "__dbterm_cp_0123456789abcdef01234567", true},
		{config.Turso, "__dbterm_cp_0123456789abcdef01234567", true},
		{config.MySQL, "_dbterm_cp_0123456789abcdef01234567_events", true},
		{config.PostgreSQL, "dbterm_cp_0123456789abcdef01234567.events", true},
		{config.SQLite, "__dbterm_cp_user_data", false},
		{config.SQLite, "__dbterm_cp_0123456789abcdef01234567_archive", false},
		{config.MySQL, "_dbterm_cp_0123456789abcdef01234567_data", false},
		{config.PostgreSQL, "public.dbterm_cp_0123456789abcdef01234567", false},
		{config.PostgreSQL, "dbterm_cp_user.events", false},
	} {
		if got := IsCheckpointJournalTable(test.engine, test.name); got != test.owned {
			t.Errorf("journal namespace %s/%q = %v, want %v", test.engine, test.name, got, test.owned)
		}
	}
}

func TestMySQLMetadataQueriesStayInCurrentDatabase(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		namespace string
	}{
		{name: "tables", query: ListTablesQuery(config.MySQL), namespace: "table_schema"},
		{name: "views", query: ListObjectsQuery(config.MySQL, ObjViews), namespace: "table_schema"},
		{name: "functions", query: ListObjectsQuery(config.MySQL, ObjFunctions), namespace: "routine_schema"},
		{name: "triggers", query: ListObjectsQuery(config.MySQL, ObjTriggers), namespace: "trigger_schema"},
		{name: "procedures", query: ListObjectsQuery(config.MySQL, ObjStoredProcedures), namespace: "routine_schema"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := strings.ToLower(tt.query)
			want := tt.namespace + " = database()"
			if !strings.Contains(query, want) {
				t.Fatalf("query does not scope to the connected database with %q:\n%s", want, tt.query)
			}
			if strings.Contains(query, " not in ") {
				t.Fatalf("query still enumerates other non-system databases:\n%s", tt.query)
			}
		})
	}
}

func TestMySQLMetadataQueriesReturnUnqualifiedNames(t *testing.T) {
	queries := map[string]string{
		"tables":     ListTablesQuery(config.MySQL),
		"views":      ListObjectsQuery(config.MySQL, ObjViews),
		"functions":  ListObjectsQuery(config.MySQL, ObjFunctions),
		"triggers":   ListObjectsQuery(config.MySQL, ObjTriggers),
		"procedures": ListObjectsQuery(config.MySQL, ObjStoredProcedures),
	}

	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(strings.ToLower(query), "concat(") {
				t.Fatalf("query still prefixes the object with its database:\n%s", query)
			}
		})
	}
}

func TestMySQLDatabaseDiscoveryHidesSystemSchemas(t *testing.T) {
	query := strings.ToLower(ListDatabasesQuery(config.MySQL))
	if !strings.Contains(query, "from information_schema.schemata") {
		t.Fatalf("discovery query does not read visible schemas:\n%s", query)
	}
	for _, systemSchema := range []string{"information_schema", "mysql", "performance_schema", "sys"} {
		if !strings.Contains(query, "'"+systemSchema+"'") {
			t.Fatalf("discovery query does not exclude %q:\n%s", systemSchema, query)
		}
	}
}

func TestPostgreSQLDatabaseDiscoveryRequiresConnectPrivilege(t *testing.T) {
	query := strings.ToLower(ListDatabasesQuery(config.PostgreSQL))
	if !strings.Contains(query, "has_database_privilege(datname, 'connect')") {
		t.Fatalf("discovery query does not filter inaccessible databases:\n%s", query)
	}
}
