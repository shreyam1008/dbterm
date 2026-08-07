package ui

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/shreyam1008/dbterm/internal/database"
)

func TestLoadDatabaseObjectInfoEscapesDefinitionText(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	summary, err := loadDatabaseObjectInfo(
		context.Background(),
		db,
		`SELECT '[red]not-a-color-tag' AS definition`,
		database.ObjFunctions,
		"demo",
	)
	if err != nil {
		t.Fatalf("load object info: %v", err)
	}
	if strings.Contains(summary, "[red]not-a-color-tag") {
		t.Fatalf("object definition contains an unescaped tview tag: %q", summary)
	}
	if !strings.Contains(summary, "not-a-color-tag") {
		t.Fatalf("object definition value is missing: %q", summary)
	}
}
