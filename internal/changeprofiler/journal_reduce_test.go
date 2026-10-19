package changeprofiler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func journalReducerPlan(name string) TablePlan {
	return TablePlan{Name: name, Included: true, KeyKind: KeyPrimary, KeyColumns: []string{"id"}, Columns: []Column{{Name: "id", Type: "INTEGER"}, {Name: "value", Type: "TEXT"}}}
}

func journalReducerRow(t testing.TB, id int, value any) []byte {
	t.Helper()
	row, _, err := encodeScannedRow([]string{"id", "value"}, []string{"INTEGER", "TEXT"}, []any{id, value})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func journalReducerRun(ctx context.Context, plans []TablePlan, events []JournalEvent) ([]JournalDiff, error) {
	var result []JournalDiff
	err := reduceJournal(ctx, plans, func(visit func(JournalEvent) error) error {
		for _, event := range events {
			if err := visit(event); err != nil {
				return err
			}
		}
		return nil
	}, func(diff JournalDiff) error {
		result = append(result, diff)
		return nil
	})
	return result, err
}

func TestJournalReducerNetChanges(t *testing.T) {
	a := journalReducerRow(t, 1, "original")
	b := journalReducerRow(t, 1, "middle")
	c := journalReducerRow(t, 1, "final")
	other := journalReducerRow(t, 2, "original")
	event := func(sequence int64, before, after []byte) JournalEvent {
		return JournalEvent{Sequence: sequence, Table: "items", Before: before, After: after}
	}
	tests := []struct {
		name   string
		events []JournalEvent
		kinds  []DiffKind
		before [][]byte
		after  [][]byte
	}{
		{"insert", []JournalEvent{event(1, nil, a)}, []DiffKind{DiffInserted}, [][]byte{nil}, [][]byte{a}},
		{"delete", []JournalEvent{event(1, a, nil)}, []DiffKind{DiffDeleted}, [][]byte{a}, [][]byte{nil}},
		{"several updates", []JournalEvent{event(1, a, b), event(2, b, c)}, []DiffKind{DiffUpdated}, [][]byte{a}, [][]byte{c}},
		{"insert then update", []JournalEvent{event(1, nil, a), event(2, a, b)}, []DiffKind{DiffInserted}, [][]byte{nil}, [][]byte{b}},
		{"update then delete", []JournalEvent{event(1, a, b), event(2, b, nil)}, []DiffKind{DiffDeleted}, [][]byte{a}, [][]byte{nil}},
		{"insert delete cancels", []JournalEvent{event(1, nil, a), event(2, a, nil)}, nil, nil, nil},
		{"return to original cancels", []JournalEvent{event(1, a, b), event(2, b, a)}, nil, nil, nil},
		{"noop cancels", []JournalEvent{event(1, a, a)}, nil, nil, nil},
		{"delete same reinsert cancels", []JournalEvent{event(1, a, nil), event(2, nil, a)}, nil, nil, nil},
		{"delete changed reinsert updates", []JournalEvent{event(1, a, nil), event(2, nil, c)}, []DiffKind{DiffUpdated}, [][]byte{a}, [][]byte{c}},
		{"cancel then change preserves original", []JournalEvent{event(1, a, b), event(2, b, a), event(3, a, c)}, []DiffKind{DiffUpdated}, [][]byte{a}, [][]byte{c}},
		{"primary key move", []JournalEvent{event(1, a, other)}, []DiffKind{DiffDeleted, DiffInserted}, [][]byte{a, nil}, [][]byte{nil, other}},
		{"primary key round trip cancels", []JournalEvent{event(1, a, other), event(2, other, a)}, nil, nil, nil},
		{"same sequence replay", []JournalEvent{event(1, a, b), event(1, a, b), event(2, b, c)}, []DiffKind{DiffUpdated}, [][]byte{a}, [][]byte{c}},
		{"same values replay", []JournalEvent{event(1, a, b), event(2, a, b), event(3, b, c)}, []DiffKind{DiffUpdated}, [][]byte{a}, [][]byte{c}},
		{"rollback sequence gaps are allowed", []JournalEvent{event(1, a, b), event(50, b, c)}, []DiffKind{DiffUpdated}, [][]byte{a}, [][]byte{c}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := journalReducerRun(context.Background(), []TablePlan{journalReducerPlan("items")}, test.events)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(test.kinds) {
				t.Fatalf("got %d changes, want %d: %+v", len(got), len(test.kinds), got)
			}
			for index, diff := range got {
				if diff.Kind != test.kinds[index] || !bytes.Equal(diff.Before, test.before[index]) || !bytes.Equal(diff.After, test.after[index]) {
					t.Fatalf("change %d = %+v, want %s before %s after %s", index, diff, test.kinds[index], test.before[index], test.after[index])
				}
				if diff.Kind == DiffUpdated && !reflect.DeepEqual(diff.ChangedColumns, []string{"value"}) {
					t.Fatalf("changed columns = %v", diff.ChangedColumns)
				}
			}
		})
	}
}

func TestJournalReducerConflictProbes(t *testing.T) {
	a := journalReducerRow(t, 1, "original")
	b := journalReducerRow(t, 1, "replacement")
	c := journalReducerRow(t, 1, "final")
	other := journalReducerRow(t, 2, "replacement")
	tests := []struct {
		name   string
		events []JournalEvent
		kinds  []DiffKind
		before [][]byte
		after  [][]byte
	}{
		{"replace hidden deletion", []JournalEvent{
			{Sequence: 1, Table: "items", Before: a, After: b, Probe: true},
			{Sequence: 2, Table: "items", After: b},
		}, []DiffKind{DiffUpdated}, [][]byte{a}, [][]byte{b}},
		{"probe final image waits for normal events", []JournalEvent{
			{Sequence: 1, Table: "items", Before: a, After: c, Probe: true},
			{Sequence: 2, Table: "items", After: b},
			{Sequence: 3, Table: "items", Before: b, After: c},
		}, []DiffKind{DiffUpdated}, [][]byte{a}, [][]byte{c}},
		{"probe without real change cancels", []JournalEvent{{Sequence: 1, Table: "items", Before: a, After: a, Probe: true}}, nil, nil, nil},
		{"upsert ordinary update plus probe", []JournalEvent{
			{Sequence: 1, Table: "items", Before: a, After: b, Probe: true},
			{Sequence: 2, Table: "items", Before: a, After: b},
		}, []DiffKind{DiffUpdated}, [][]byte{a}, [][]byte{b}},
		{"duplicate candidate observations", []JournalEvent{
			{Sequence: 1, Table: "items", Before: a, After: b, Probe: true},
			{Sequence: 2, Table: "items", Before: a, After: b, Probe: true},
			{Sequence: 3, Table: "items", After: b},
		}, []DiffKind{DiffUpdated}, [][]byte{a}, [][]byte{b}},
		{"unique conflict evicts other key", []JournalEvent{
			{Sequence: 1, Table: "items", Before: a, Probe: true},
			{Sequence: 2, Table: "items", After: other},
		}, []DiffKind{DiffDeleted, DiffInserted}, [][]byte{a, nil}, [][]byte{nil, other}},
		{"later probe preserves original", []JournalEvent{
			{Sequence: 1, Table: "items", Before: a, After: b},
			{Sequence: 2, Table: "items", Before: b, After: c, Probe: true},
			{Sequence: 3, Table: "items", After: c},
		}, []DiffKind{DiffUpdated}, [][]byte{a}, [][]byte{c}},
		{"insert then hidden deletion cancels", []JournalEvent{
			{Sequence: 1, Table: "items", After: a},
			{Sequence: 2, Table: "items", Before: a, Probe: true},
		}, nil, nil, nil},
		{"collated identity move", []JournalEvent{
			{Sequence: 1, Table: "items", Before: a, After: other, Probe: true},
			{Sequence: 2, Table: "items", Before: a, After: other},
		}, []DiffKind{DiffDeleted, DiffInserted}, [][]byte{a, nil}, [][]byte{nil, other}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := journalReducerRun(context.Background(), []TablePlan{journalReducerPlan("items")}, test.events)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(test.kinds) {
				t.Fatalf("got %d changes, want %d: %+v", len(got), len(test.kinds), got)
			}
			for index, diff := range got {
				if diff.Kind != test.kinds[index] || !bytes.Equal(diff.Before, test.before[index]) || !bytes.Equal(diff.After, test.after[index]) {
					t.Fatalf("change %d = %+v, want %s before %s after %s", index, diff, test.kinds[index], test.before[index], test.after[index])
				}
			}
		})
	}
}

func TestJournalReducerCanonicalRows(t *testing.T) {
	a := journalReducerRow(t, 1, "nul\x00unicode 👩🏽‍💻")
	// A source-side jsonb encoder can reorder JSON object keys and include
	// explicit empty values or a different column order without changing data.
	b := []byte(`{ "c": [{"v":"nul\u0000unicode 👩🏽‍💻","t":"text","k":"text","n":"value"},{"v":"1","k":"int","n":"id","t":"integer"}] }`)
	got, err := journalReducerRun(context.Background(), []TablePlan{journalReducerPlan("items")}, []JournalEvent{{Sequence: 1, Table: "items", Before: a, After: b}})
	if err != nil || len(got) != 0 {
		t.Fatalf("equal canonical rows changed: %+v, %v", got, err)
	}
	// SQL NULL, the literal string NULL and empty text stay distinct.
	// Binary values are exercised with BLOB affinity below.
	for _, value := range []any{nil, "NULL", ""} {
		after := journalReducerRow(t, 1, value)
		got, err := journalReducerRun(context.Background(), []TablePlan{journalReducerPlan("items")}, []JournalEvent{{Sequence: 1, Table: "items", Before: a, After: after}})
		if err != nil || len(got) != 1 || !bytes.Equal(got[0].After, after) {
			t.Fatalf("value %#v: %+v, %v", value, got, err)
		}
	}
}

func TestJournalReducerRowIDAndCompositeIdentity(t *testing.T) {
	plan := TablePlan{Name: "items", Included: true, KeyKind: KeyRowID, KeyColumns: []string{"__dbterm_rowid"}, Columns: []Column{{Name: "value", Type: "TEXT"}}}
	a, _, _ := encodeScannedRow([]string{"__dbterm_rowid", "value"}, []string{"INTEGER", "TEXT"}, []any{int64(1), "old"})
	b, _, _ := encodeScannedRow([]string{"__dbterm_rowid", "value"}, []string{"INTEGER", "TEXT"}, []any{int64(1), "new"})
	got, err := journalReducerRun(context.Background(), []TablePlan{plan}, []JournalEvent{{Sequence: 1, Table: "items", Before: a, After: b}})
	if err != nil || len(got) != 1 || got[0].Kind != DiffUpdated || !strings.Contains(string(got[0].Key), "__dbterm_rowid") {
		t.Fatalf("rowid identity: %+v, %v", got, err)
	}
	plan = journalReducerPlan("items")
	plan.KeyKind, plan.KeyColumns = KeyUnique, []string{"id", "value"}
	got, err = journalReducerRun(context.Background(), []TablePlan{plan}, []JournalEvent{{Sequence: 1, Table: "items", Before: journalReducerRow(t, 1, "old"), After: journalReducerRow(t, 1, "new")}})
	if err != nil || len(got) != 2 {
		t.Fatalf("composite key move: %+v, %v", got, err)
	}
}

func TestJournalReducerRejectsInvalidCoverage(t *testing.T) {
	a, b, c := journalReducerRow(t, 1, "a"), journalReducerRow(t, 1, "b"), journalReducerRow(t, 1, "c")
	tests := []struct {
		name   string
		events []JournalEvent
	}{
		{"causal gap", []JournalEvent{{Sequence: 1, Table: "items", Before: a, After: b}, {Sequence: 2, Table: "items", Before: c}}},
		{"out of order", []JournalEvent{{Sequence: 2, Table: "items", After: a}, {Sequence: 1, Table: "items", Before: a}}},
		{"sequence reused", []JournalEvent{{Sequence: 1, Table: "items", After: a}, {Sequence: 1, Table: "items", After: b}}},
		{"unknown table", []JournalEvent{{Sequence: 1, Table: "wrong", After: a}}},
		{"empty event", []JournalEvent{{Sequence: 1, Table: "items"}}},
		{"invalid JSON", []JournalEvent{{Sequence: 1, Table: "items", After: []byte("garbage")}}},
		{"missing cells", []JournalEvent{{Sequence: 1, Table: "items", After: []byte(`{"c":[]}`)}}},
		{"duplicate cells", []JournalEvent{{Sequence: 1, Table: "items", After: []byte(`{"c":[{"n":"id","k":"int","v":"1"},{"n":"id","k":"int","v":"1"},{"n":"value","k":"text"}]}`)}}},
		{"NULL identity", []JournalEvent{{Sequence: 1, Table: "items", After: []byte(`{"c":[{"n":"id","k":"null"},{"n":"value","k":"text"}]}`)}}},
		{"probe without candidate", []JournalEvent{{Sequence: 1, Table: "items", After: a, Probe: true}}},
		{"probe causal gap", []JournalEvent{{Sequence: 1, Table: "items", Before: a, After: b}, {Sequence: 2, Table: "items", Before: c, After: c, Probe: true}}},
		{"contradictory final lookups", []JournalEvent{{Sequence: 1, Table: "items", Before: a, After: b, Probe: true}, {Sequence: 2, Table: "items", Before: a, After: c, Probe: true}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := journalReducerRun(context.Background(), []TablePlan{journalReducerPlan("items")}, test.events)
			if err == nil || len(got) != 0 {
				t.Fatalf("invalid journal produced %+v, %v", got, err)
			}
		})
	}
	plan := journalReducerPlan("items")
	plan.KeyKind = KeyFullRow
	if _, err := journalReducerRun(context.Background(), []TablePlan{plan}, nil); err == nil {
		t.Fatal("full row identity unexpectedly allowed")
	}
}

func TestJournalReducerDeterminismAndCancellation(t *testing.T) {
	plans := []TablePlan{journalReducerPlan("z"), journalReducerPlan("a")}
	var events []JournalEvent
	for index, table := range []string{"z", "a", "z", "a"} {
		events = append(events, JournalEvent{Sequence: int64(index + 1), Table: table, After: journalReducerRow(t, 4-index, "value")})
	}
	got, err := journalReducerRun(context.Background(), plans, events)
	if err != nil {
		t.Fatal(err)
	}
	for index := 1; index < len(got); index++ {
		if got[index-1].Table > got[index].Table || (got[index-1].Table == got[index].Table && bytes.Compare(got[index-1].Key, got[index].Key) > 0) {
			t.Fatalf("unstable output ordering: %+v", got)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := journalReducerRun(ctx, plans, events); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	wantErr := errors.New("read failed")
	err = reduceJournal(context.Background(), plans, func(func(JournalEvent) error) error { return wantErr }, func(JournalDiff) error { t.Fatal("emitted after failed read"); return nil })
	if !errors.Is(err, wantErr) {
		t.Fatalf("read error = %v", err)
	}
	count := 0
	err = reduceJournal(context.Background(), plans, func(visit func(JournalEvent) error) error {
		for _, event := range events {
			if err := visit(event); err != nil {
				return err
			}
		}
		return nil
	}, func(JournalDiff) error { count++; return wantErr })
	if !errors.Is(err, wantErr) || count != 1 {
		t.Fatalf("emit failure = %v, calls = %d", err, count)
	}
}

func BenchmarkJournalReducerRepeatedChanges(b *testing.B) {
	plan := journalReducerPlan("items")
	const eventCount = 10_000
	rows := [][]byte{journalReducerRow(b, 1, "old"), journalReducerRow(b, 1, "new")}
	b.ReportAllocs()
	b.SetBytes(eventCount)
	for iteration := 0; iteration < b.N; iteration++ {
		count := 0
		err := reduceJournal(context.Background(), []TablePlan{plan}, func(visit func(JournalEvent) error) error {
			for index := 0; index < eventCount+1; index++ {
				if err := visit(JournalEvent{Sequence: int64(index + 1), Table: "items", Before: rows[index%2], After: rows[(index+1)%2]}); err != nil {
					return err
				}
			}
			return nil
		}, func(diff JournalDiff) error {
			count++
			if diff.Kind != DiffUpdated {
				return fmt.Errorf("wrong kind %s", diff.Kind)
			}
			return nil
		})
		if err != nil || count != 1 {
			b.Fatalf("reduce: %v, changes: %d", err, count)
		}
	}
}

func TestJournalReducerExactChangedColumns(t *testing.T) {
	plan := TablePlan{Name: "items", Included: true, KeyKind: KeyPrimary, KeyColumns: []string{"id"}, Columns: []Column{{Name: "id", Type: "INTEGER"}, {Name: "payload", Type: "BLOB"}, {Name: "nullable", Type: "TEXT"}, {Name: "huge", Type: "TEXT"}}}
	before, _, err := encodeScannedRow([]string{"id", "payload", "nullable", "huge"}, []string{"INTEGER", "BLOB", "TEXT", "TEXT"}, []any{int64(9223372036854775807), []byte{0, 255}, nil, strings.Repeat("long 👩🏽‍💻", 1000)})
	if err != nil {
		t.Fatal(err)
	}
	var row encodedRow
	if err := json.Unmarshal(before, &row); err != nil {
		t.Fatal(err)
	}
	row.Cells[1].Data = "AP4="
	row.Cells[2].Kind, row.Cells[2].Data = "text", "NULL"
	after, _ := json.Marshal(row)
	got, err := journalReducerRun(context.Background(), []TablePlan{plan}, []JournalEvent{{Sequence: 1, Table: "items", Before: before, After: after}})
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0].ChangedColumns, []string{"payload", "nullable"}) || !bytes.Equal(got[0].Before, before) || !bytes.Equal(got[0].After, after) {
		t.Fatalf("lossless changed columns = %+v, %v", got, err)
	}
}

func TestJournalReducerRandomizedNetSemantics(t *testing.T) {
	// Compare a stream with a simple in-memory reference model. Production
	// reduction never needs either of these full baseline/current snapshots.
	for seed := int64(1); seed <= 5; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			plans := []TablePlan{journalReducerPlan("alpha"), journalReducerPlan("beta")}
			type identity struct {
				table string
				id    int
			}
			baseline, current := map[identity][]byte{}, map[identity][]byte{}
			for _, plan := range plans {
				for id := 0; id < 25; id++ {
					key := identity{plan.Name, id}
					baseline[key] = journalReducerRow(t, id, "original")
					current[key] = baseline[key]
				}
			}
			var events []JournalEvent
			appendEvent := func(event JournalEvent) {
				event.Sequence = int64(len(events) + 1)
				events = append(events, event)
			}
			for step := 0; step < 2000; step++ {
				key := identity{plans[rng.Intn(len(plans))].Name, rng.Intn(50)}
				before := current[key]
				event := JournalEvent{Table: key.table, Before: before}
				switch rng.Intn(5) {
				case 0:
					if before == nil {
						continue
					}
					delete(current, key)
				case 1:
					if before == nil {
						continue
					}
					dest := identity{key.table, rng.Intn(50)}
					if current[dest] != nil {
						continue
					}
					event.After = journalReducerRow(t, dest.id, fmt.Sprintf("moved %d", step))
					delete(current, key)
					current[dest] = event.After
				case 2:
					event.After = journalReducerRow(t, key.id, "original")
					current[key] = event.After
				default:
					event.After = journalReducerRow(t, key.id, fmt.Sprintf("changed %d\x00", rng.Intn(4)))
					current[key] = event.After
				}
				appendEvent(event)
				if rng.Intn(8) == 0 {
					appendEvent(event)
				}
			}
			got, err := journalReducerRun(context.Background(), plans, events)
			if err != nil {
				t.Fatal(err)
			}
			want := map[journalIdentity]JournalDiff{}
			for _, plan := range plans {
				for id := 0; id < 50; id++ {
					key := identity{plan.Name, id}
					before, after := baseline[key], current[key]
					if bytes.Equal(before, after) {
						continue
					}
					payload := after
					if payload == nil {
						payload = before
					}
					_, encodedKey, err := canonicalJournalRow(plan, payload)
					if err != nil {
						t.Fatal(err)
					}
					kind := DiffUpdated
					if before == nil {
						kind = DiffInserted
					} else if after == nil {
						kind = DiffDeleted
					}
					want[journalIdentity{plan.Name, string(encodedKey)}] = JournalDiff{Kind: kind, Before: before, After: after}
				}
			}
			if len(got) != len(want) {
				t.Fatalf("got %d net changes, want %d", len(got), len(want))
			}
			for _, diff := range got {
				expected, found := want[journalIdentity{diff.Table, string(diff.Key)}]
				if !found || diff.Kind != expected.Kind || !bytes.Equal(diff.Before, expected.Before) || !bytes.Equal(diff.After, expected.After) {
					t.Fatalf("unexpected change %+v, expected %+v", diff, expected)
				}
			}
		})
	}
}

func TestJournalReducerRandomizedConflictProbes(t *testing.T) {
	// Simulate SQLite's REPLACE/IGNORE paths, including deletion of a second
	// unique-key conflict without an AFTER DELETE event. Probe final images
	// are obtained only after the entire write stream has completed.
	for seed := int64(1); seed <= 3; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			plan := journalReducerPlan("items")
			baseline, current := map[int][]byte{}, map[int][]byte{}
			for id := 0; id < 25; id++ {
				baseline[id] = journalReducerRow(t, id, "original")
				current[id] = baseline[id]
			}
			var events []JournalEvent
			probes := map[int]int{}
			appendEvent := func(event JournalEvent) {
				event.Sequence, event.Table = int64(len(events)+1), "items"
				events = append(events, event)
			}
			probe := func(id int) {
				if row := current[id]; row != nil {
					probes[len(events)] = id
					appendEvent(JournalEvent{Before: row, Probe: true})
				}
			}
			for step := 0; step < 1000; step++ {
				id := rng.Intn(50)
				before := current[id]
				after := journalReducerRow(t, id, fmt.Sprintf("value %d", rng.Intn(4)))
				switch rng.Intn(6) {
				case 0: // Ordinary insert/update.
					appendEvent(JournalEvent{Before: before, After: after})
					current[id] = after
				case 1: // INSERT OR REPLACE, without a delete event.
					probe(id)
					appendEvent(JournalEvent{After: after})
					current[id] = after
				case 2: // INSERT OR IGNORE, with a speculative candidate.
					probe(id)
					if before == nil {
						appendEvent(JournalEvent{After: after})
						current[id] = after
					}
				case 3: // A second unique key evicts another identity.
					evicted := rng.Intn(50)
					probe(id)
					probe(evicted)
					delete(current, evicted)
					appendEvent(JournalEvent{After: after})
					current[id] = after
				case 4: // UPDATE OR REPLACE moves into an occupied key.
					if before == nil {
						continue
					}
					dest := rng.Intn(50)
					probe(dest)
					after = journalReducerRow(t, dest, fmt.Sprintf("moved %d", step))
					appendEvent(JournalEvent{Before: before, After: after})
					delete(current, id)
					current[dest] = after
				default:
					if before != nil {
						appendEvent(JournalEvent{Before: before})
						delete(current, id)
					}
				}
			}
			for eventIndex, id := range probes {
				events[eventIndex].After = current[id]
			}
			got, err := journalReducerRun(context.Background(), []TablePlan{plan}, events)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]JournalDiff{}
			for id := 0; id < 50; id++ {
				before, after := baseline[id], current[id]
				if bytes.Equal(before, after) {
					continue
				}
				payload := after
				if payload == nil {
					payload = before
				}
				_, key, err := canonicalJournalRow(plan, payload)
				if err != nil {
					t.Fatal(err)
				}
				kind := DiffUpdated
				if before == nil {
					kind = DiffInserted
				} else if after == nil {
					kind = DiffDeleted
				}
				want[string(key)] = JournalDiff{Kind: kind, Before: before, After: after}
			}
			if len(got) != len(want) {
				t.Fatalf("got %d net changes, want %d", len(got), len(want))
			}
			for _, diff := range got {
				expected, found := want[string(diff.Key)]
				if !found || diff.Kind != expected.Kind || !bytes.Equal(diff.Before, expected.Before) || !bytes.Equal(diff.After, expected.After) {
					t.Fatalf("unexpected change %+v, expected %+v", diff, expected)
				}
			}
		})
	}
}
