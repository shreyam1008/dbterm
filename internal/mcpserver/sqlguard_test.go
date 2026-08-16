package mcpserver

import (
	"strings"
	"testing"
)

func TestValidateReadOnlySQL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{name: "select", query: `SELECT id, 'delete is text' FROM users;`},
		{name: "cte", query: `WITH chosen AS (SELECT id FROM users) SELECT * FROM chosen`},
		{name: "safe pragma", query: `PRAGMA table_info("users")`},
		{name: "comment", query: "-- DELETE FROM users\nSELECT 1"},
		{name: "delete", query: `DELETE FROM users`, want: "statement type DELETE"},
		{name: "data changing cte", query: `WITH changed AS (DELETE FROM users RETURNING *) SELECT * FROM changed`, want: "keyword DELETE"},
		{name: "multiple", query: `SELECT 1; SELECT 2`, want: "only one"},
		{name: "explain analyze", query: `EXPLAIN ANALYZE SELECT * FROM users`, want: "EXPLAIN is limited"},
		{name: "pragma assignment", query: `PRAGMA foreign_keys=OFF`, want: "introspection PRAGMAs"},
		{name: "pragma parenthesized setter", query: `PRAGMA foreign_keys(ON)`, want: "does not accept an argument"},
		{name: "pragma missing identifier", query: `PRAGMA table_info`, want: "requires one"},
		{name: "pragma unsafe identifier", query: `PRAGMA table_info(users, visits)`, want: "safe table or index identifier"},
		{name: "show restricted", query: `SHOW VARIABLES`, want: "statement type SHOW"},
		{name: "mysql executable comment", query: `SELECT 1 /*!; DELETE FROM users */`, want: "executable comments"},
		{name: "mariadb executable comment", query: `SELECT 1 /*M!100100 ; DELETE FROM users */`, want: "executable comments"},
		{name: "mariadb lowercase executable comment", query: `SELECT 1 /*m! ; DELETE FROM users */`, want: "executable comments"},
		{name: "select into", query: `SELECT * INTO copied FROM users`, want: "keyword INTO"},
		{name: "unterminated", query: `SELECT 'secret`, want: "unterminated"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateReadOnlySQL(test.query, defaultMaxQueryBytes)
			if test.want == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateExplainableSQLRejectsNonSelect(t *testing.T) {
	t.Parallel()
	if err := validateExplainableSQL("SHOW TABLES", defaultMaxQueryBytes); err == nil {
		t.Fatal("expected SHOW to be rejected for EXPLAIN")
	}
}
