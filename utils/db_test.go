package utils

import (
	"strings"
	"testing"

	"github.com/shreyam1008/dbterm/config"
)

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
