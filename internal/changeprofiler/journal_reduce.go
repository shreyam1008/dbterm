package changeprofiler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// JournalDiff is a net change between a journal's two boundaries. Row images
// remain in their lossless encoded form until the store compresses them; going
// through display Values would round floats and change binary representations.
type JournalDiff struct {
	Table          string
	Kind           DiffKind
	Key            []byte
	Before         []byte
	After          []byte
	ChangedColumns []string
}

type journalIdentity struct {
	table string
	key   string
}

type journalRowState struct {
	key           []byte
	before, after []byte
	probed        bool
	finalSet      bool
	final         []byte
}

// reduceJournal visits recorded writes, never the application tables or a
// baseline copy. Memory depends on touched identities, rather than database
// size or event count. Keeping an identity even after its net change cancels
// preserves both causality checks and its original before image if it changes
// again later in the interval.
func reduceJournal(ctx context.Context, plans []TablePlan, read func(func(JournalEvent) error) error, emit func(JournalDiff) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	byTable := make(map[string]TablePlan, len(plans))
	for _, plan := range plans {
		if _, duplicate := byTable[plan.Name]; duplicate {
			return fmt.Errorf("duplicate table %q in checkpoint journal plans", plan.Name)
		}
		if plan.KeyKind == KeyFullRow || len(plan.KeyColumns) == 0 {
			return fmt.Errorf("checkpoint journal requires a stable identity on %s; use snapshot comparison for this table", plan.Name)
		}
		byTable[plan.Name] = plan
	}
	states := make(map[journalIdentity]*journalRowState)
	var last JournalEvent
	haveLast := false

	apply := func(identity journalIdentity, key, before, after []byte, sequence int64) error {
		state, found := states[identity]
		if !found {
			states[identity] = &journalRowState{key: key, before: before, after: after}
			return nil
		}
		switch {
		case bytes.Equal(state.after, before):
			state.after = after
		case bytes.Equal(state.after, after):
			// An identical event can be replayed without making a false change.
		case state.probed:
			// SQLite can silently delete a conflicting row during REPLACE.
			// A pre-write probe covers that gap, and its final indexed lookup
			// is applied after all ordinary events below.
			state.after = after
		default:
			return fmt.Errorf("checkpoint journal continuity lost for %s at event %d: before image does not match the preceding event", identity.table, sequence)
		}
		return nil
	}
	setFinal := func(identity journalIdentity, key, row []byte) error {
		state, found := states[identity]
		if !found {
			state = &journalRowState{key: key}
			states[identity] = state
		}
		if state.finalSet && !bytes.Equal(state.final, row) {
			return fmt.Errorf("inconsistent final point lookups for checkpoint journal table %s", identity.table)
		}
		state.finalSet, state.final = true, row
		return nil
	}

	err := read(func(event JournalEvent) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		plan, found := byTable[event.Table]
		if !found {
			return fmt.Errorf("unexpected table %q in checkpoint journal", event.Table)
		}
		before, beforeKey, err := canonicalJournalRow(plan, event.Before)
		if err != nil {
			return fmt.Errorf("decode %s before image at event %d: %w", event.Table, event.Sequence, err)
		}
		after, afterKey, err := canonicalJournalRow(plan, event.After)
		if err != nil {
			return fmt.Errorf("decode %s after image at event %d: %w", event.Table, event.Sequence, err)
		}
		event.Before, event.After = before, after
		if event.Sequence < 0 || (haveLast && event.Sequence < last.Sequence) {
			return fmt.Errorf("checkpoint journal events are out of sequence at event %d", event.Sequence)
		}
		if haveLast && event.Sequence == last.Sequence {
			if event.Table == last.Table && event.Probe == last.Probe && bytes.Equal(before, last.Before) && bytes.Equal(after, last.After) {
				return nil
			}
			return fmt.Errorf("conflicting checkpoint journal events share sequence %d", event.Sequence)
		}
		last, haveLast = event, true
		if before == nil && after == nil {
			return fmt.Errorf("checkpoint journal event %d has neither a before nor an after image", event.Sequence)
		}
		beforeID := journalIdentity{table: event.Table, key: string(beforeKey)}
		afterID := journalIdentity{table: event.Table, key: string(afterKey)}
		if event.Probe {
			if before == nil {
				return fmt.Errorf("checkpoint journal probe %d has no before image", event.Sequence)
			}
			state, found := states[beforeID]
			if !found {
				state = &journalRowState{key: beforeKey, before: before, after: before}
				states[beforeID] = state
			} else if !state.probed && !bytes.Equal(state.after, before) {
				return fmt.Errorf("checkpoint journal continuity lost for %s at probe %d", event.Table, event.Sequence)
			}
			state.probed = true
			// A collated key can change spelling while a final point lookup
			// still finds it. Preserve the old/new identities independently.
			if after != nil && beforeID == afterID {
				return setFinal(beforeID, beforeKey, after)
			}
			if err := setFinal(beforeID, beforeKey, nil); err != nil {
				return err
			}
			if after != nil {
				return setFinal(afterID, afterKey, after)
			}
			return nil
		}
		if before != nil && after != nil && beforeID == afterID {
			return apply(beforeID, beforeKey, before, after, event.Sequence)
		}
		// Key moves are a removal at the old identity and an insertion at
		// the new one, so repeated moves and delete/reinsert chains coalesce.
		if before != nil {
			if err := apply(beforeID, beforeKey, before, nil, event.Sequence); err != nil {
				return err
			}
		}
		if after != nil {
			return apply(afterID, afterKey, nil, after, event.Sequence)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	identities := make([]journalIdentity, 0, len(states))
	for identity, state := range states {
		if state.finalSet {
			state.after = state.final
		}
		if !bytes.Equal(state.before, state.after) {
			identities = append(identities, identity)
		}
	}
	sort.Slice(identities, func(i, j int) bool {
		if identities[i].table != identities[j].table {
			return identities[i].table < identities[j].table
		}
		return identities[i].key < identities[j].key
	})
	for _, identity := range identities {
		if err := ctx.Err(); err != nil {
			return err
		}
		state := states[identity]
		diff := JournalDiff{Table: identity.table, Key: state.key, Before: state.before, After: state.after, ChangedColumns: []string{}}
		switch {
		case state.before == nil:
			diff.Kind = DiffInserted
		case state.after == nil:
			diff.Kind = DiffDeleted
		default:
			diff.Kind = DiffUpdated
			diff.ChangedColumns, err = changedColumns(state.before, state.after)
			if err != nil {
				return err
			}
		}
		if err := emit(diff); err != nil {
			return err
		}
	}
	return nil
}

// canonicalJournalRow removes JSON object/cell ordering differences between
// native database encoders and fixes type labels to the checkpoint's schema.
// Rejecting missing or duplicate cells prevents malformed images from silently
// collapsing several rows onto an empty/partial key.
func canonicalJournalRow(plan TablePlan, payload []byte) ([]byte, []byte, error) {
	if len(payload) == 0 {
		return nil, nil, nil
	}
	var row encodedRow
	if err := json.Unmarshal(payload, &row); err != nil {
		return nil, nil, err
	}
	cells := make(map[string]encodedCell, len(row.Cells))
	for _, cell := range row.Cells {
		if _, duplicate := cells[cell.Name]; duplicate {
			return nil, nil, fmt.Errorf("duplicate column %q", cell.Name)
		}
		if cell.Kind == "" {
			return nil, nil, fmt.Errorf("column %q has no value kind", cell.Name)
		}
		cells[cell.Name] = cell
	}
	ordered := encodedRow{Cells: make([]encodedCell, 0, len(plan.Columns)+1)}
	appendColumn := func(name, columnType string) error {
		cell, found := cells[name]
		if !found {
			return fmt.Errorf("missing column %q", name)
		}
		cell.Type = columnType
		ordered.Cells = append(ordered.Cells, cell)
		delete(cells, name)
		return nil
	}
	if plan.KeyKind == KeyRowID {
		if err := appendColumn("__dbterm_rowid", "INTEGER"); err != nil {
			return nil, nil, err
		}
	}
	for _, column := range plan.Columns {
		if err := appendColumn(column.Name, column.Type); err != nil {
			return nil, nil, err
		}
	}
	if len(cells) != 0 {
		return nil, nil, fmt.Errorf("row contains columns outside the checkpoint schema")
	}
	for _, name := range plan.KeyColumns {
		found := false
		for _, cell := range ordered.Cells {
			if cell.Name == name {
				if cell.Kind == "null" {
					return nil, nil, fmt.Errorf("identity column %q is NULL", name)
				}
				found = true
				break
			}
		}
		if !found {
			return nil, nil, fmt.Errorf("missing identity column %q", name)
		}
	}
	canonical, err := json.Marshal(ordered)
	if err != nil {
		return nil, nil, err
	}
	key, _, err := encodeRowKey(ordered, plan.KeyColumns, plan.KeyKind, 0)
	return canonical, key, err
}
