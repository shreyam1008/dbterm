package changeprofiler

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shreyam1008/dbterm/internal/config"
)

// Run with -benchtime=1x for a reproducible workflow matrix. Setup and fixture
// mutation are excluded from timing; capture and scan include real disk I/O.
func BenchmarkCheckpointWorkflow(b *testing.B) {
	for _, rows := range []int{1000, 10000, 100000} {
		for _, payloadBytes := range []int{128, 4096} {
			label := fmt.Sprintf("rows=%d/payload=%d", rows, payloadBytes)
			b.Run(label, func(b *testing.B) {
				for _, operation := range []string{"capture", "scan-unchanged", "scan-one-percent", "scan-all-changed"} {
					b.Run(operation, func(b *testing.B) {
						benchmarkCheckpointOperation(b, rows, payloadBytes, operation)
					})
				}
			})
		}
	}
}

func benchmarkCheckpointOperation(b *testing.B, rows, payloadBytes int, operation string) {
	b.StopTimer()
	ctx := context.Background()
	directory := b.TempDir()
	source, err := sql.Open("sqlite", filepath.Join(directory, "source.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer source.Close()
	if _, err := source.Exec(`CREATE TABLE items(id INTEGER PRIMARY KEY, text_value TEXT NOT NULL, nullable TEXT, binary_value BLOB, amount REAL)`); err != nil {
		b.Fatal(err)
	}
	tx, err := source.Begin()
	if err != nil {
		b.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO items VALUES(?,?,NULL,?,?)`)
	if err != nil {
		b.Fatal(err)
	}
	payload := strings.Repeat("checkpoint-data-", payloadBytes/16+1)
	payload = payload[:payloadBytes]
	for index := 1; index <= rows; index++ {
		if _, err := statement.Exec(index, payload, []byte{0, 255, byte(index)}, float64(index)/7); err != nil {
			b.Fatal(err)
		}
	}
	_ = statement.Close()
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	plans, err := Preflight(ctx, source, config.SQLite)
	if err != nil {
		b.Fatal(err)
	}
	for index := range plans {
		plans[index].Included = true
	}
	b.ReportAllocs()
	b.SetBytes(int64(rows * payloadBytes))
	for iteration := 0; iteration < b.N; iteration++ {
		store, err := OpenStore(filepath.Join(directory, fmt.Sprintf("profiler-%d.db", iteration)))
		if err != nil {
			b.Fatal(err)
		}
		request := StartRequest{Name: "benchmark", ConnectionKey: "benchmark", Engine: config.SQLite, Tables: plans}
		if operation == "capture" {
			b.StartTimer()
			_, err = store.Start(ctx, source, request, nil)
			b.StopTimer()
		} else {
			anchor, startErr := store.Start(ctx, source, request, nil)
			if startErr != nil {
				b.Fatal(startErr)
			}
			switch operation {
			case "scan-one-percent":
				_, err = source.Exec(`UPDATE items SET nullable=? WHERE id%100=0`, fmt.Sprintf("change-%d", iteration))
			case "scan-all-changed":
				_, err = source.Exec(`UPDATE items SET nullable=?`, fmt.Sprintf("change-%d", iteration))
			}
			if err != nil {
				b.Fatal(err)
			}
			b.StartTimer()
			_, err = store.Scan(ctx, source, anchor.ID, false, nil)
			b.StopTimer()
		}
		if err != nil {
			b.Fatal(err)
		}
		_ = store.Close()
	}
}

func BenchmarkRowIdentity(b *testing.B) {
	for _, size := range []int{128, 4096, 65536} {
		values := []any{int64(42), strings.Repeat("checkpoint data ", size/16+1)}
		for _, shared := range []bool{false, true} {
			b.Run(fmt.Sprintf("payload=%d/shared=%v", size, shared), func(b *testing.B) {
				b.ReportAllocs()
				for iteration := 0; iteration < b.N; iteration++ {
					if shared {
						if _, _, _, _, err := encodeScannedIdentity([]string{"id", "payload"}, []string{"INTEGER", "TEXT"}, values, []string{"id"}, KeyPrimary, nil); err != nil {
							b.Fatal(err)
						}
					} else {
						row, _, err := encodeScannedRow([]string{"id", "payload"}, []string{"INTEGER", "TEXT"}, values)
						if err != nil {
							b.Fatal(err)
						}
						if _, _, err := encodeKey(row, []string{"id"}, KeyPrimary, 0); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}
