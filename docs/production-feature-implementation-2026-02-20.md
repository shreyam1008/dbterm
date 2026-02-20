# dbterm Production Implementation Notes
Date: 2026-02-20

## Scope implemented
- Kept: per-connection query history.
- Removed from active UX scope: saved query/snippet library.
- Added: multi-row result selection + selected/all CSV export.
- Added: configurable keymap system with persistent settings file.
- Added: dashboard-accessible settings page.
- Added: SQL dump import workflow for PostgreSQL/MySQL.
- Updated: README, in-app help, and website guide/home pages.

## 1) Multi-row selection and CSV export

### Behavior
- `Space` in Results toggles selection for the current row.
- `Alt+A` selects all currently displayed rows.
- `Alt+C` clears selected rows.
- `Alt+E` exports:
  - selected rows when selection exists,
  - otherwise all currently displayed rows.
- Results title and status bar show selection count.

### Implementation
- `ui/results.go`
  - Row selection state is stored on table cells via `TableCell.Reference`.
  - Added selection helpers:
    - `toggleCurrentResultRowSelection`
    - `selectAllResultRows`
    - `clearResultRowSelection`
    - `selectedResultRows`
    - `selectedResultRowCount`
  - Export path now uses selected rows first.
- `ui/app.go`
  - `Space` in Results now toggles row selection (Enter still opens row details).
  - Keymap actions wired for select-all and clear-selection.

## 2) Settings config + configurable keymap

### Config path
- `~/.config/dbterm/settings.json`

### Implementation
- `config/settings.go`
  - Added settings model and defaults:
    - keymap action names
    - default bindings
  - Added load/save behavior with defaults fallback.
- `ui/keymap.go`
  - Added binding normalization and event-to-action resolver.
  - Added duplicate/unknown binding validation.
- `ui/app.go`
  - Loads settings on startup (warning-only fallback).
  - Builds runtime keymap resolver from settings.
  - Routes key actions through resolver.

### Supported keymap actions
- `focus_tables`
- `focus_query`
- `focus_results`
- `dashboard`
- `help`
- `services`
- `fullscreen`
- `backup`
- `export_csv`
- `history`
- `settings`
- `import_dump`
- `select_all`
- `clear_selection`

## 3) Settings page from dashboard

### Behavior
- `G` from Dashboard opens Settings.
- Settings page edits key bindings and validates before save.
- `Ctrl+S` saves.
- `Esc` returns to Dashboard.
- Saved settings are applied immediately in the running session.

### Implementation
- `ui/settings.go` (new)
- `ui/dashboard.go`
  - Added dashboard `G` shortcut.
  - Added footer hints for Settings.

## 4) SQL dump import (PostgreSQL/MySQL)

### Behavior
- `Alt+I` opens import modal (workspace).
- Requires active PostgreSQL or MySQL connection.
- User provides `.sql` file path and chooses stop-on-error behavior.
- Streams import output in a progress modal.
- On completion, attempts `refreshData()`.

### Implementation
- `ui/import.go` (new)
  - PostgreSQL import via `psql`.
  - MySQL import via `mysql`.
  - Path validation + readable file checks.
  - Timeout and streamed output handling.
- `ui/app.go`
  - Added keymap action handling for import.

## 5) Query history kept, snippets UI removed

### Behavior
- Query history remains per-connection and persisted.
- Saved-query/snippet UI shortcuts and active flows removed.

### Implementation
- `ui/query.go`
  - History append on successful read/write execution.
- `ui/query_library.go`
  - Retained history modal flow.
  - Removed saved-query form/list flows.
- `ui/app.go`, `ui/help.go`, `README.md`
  - Removed saved-query shortcut exposure.

## 6) Documentation and site updates

### README
- Updated shortcuts and feature descriptions.
- Added settings/keymap section and SQL import notes.

### In-app help
- Updated shortcut table for:
  - settings
  - row selection
  - selected/all export
  - SQL import

### Website
- `site/src/pages/index.astro`
- `site/src/pages/guide.astro`
- Updated feature/shortcut rows and import/settings descriptions.

## 7) Research used (official docs)

- PostgreSQL `psql` manual (file execution, variable handling, startup behavior):
  - https://www.postgresql.org/docs/current/app-psql.html
- PostgreSQL `pg_dump` / restore guidance (plain SQL scripts loaded via `psql`):
  - https://www.postgresql.org/docs/current/app-pgdump.html
- MySQL command options reference (`--force`, defaults handling):
  - https://dev.mysql.com/doc/refman/8.0/en/mysql-command-options.html
- `tview` table behavior for row/column selection model:
  - https://pkg.go.dev/github.com/rivo/tview
- `tcell` input model + modifier caveats:
  - https://pkg.go.dev/github.com/gdamore/tcell/v2

## 8) Verification run

- `gofmt -w ...` on all changed Go files.
- `GOCACHE=/tmp/dbterm-go-build go test ./...` passed.
- `GOCACHE=/tmp/dbterm-go-build go build ./...` passed (with non-fatal module stat-cache permission warning in sandbox).

## 9) Operational notes

- Import requires `psql` and/or `mysql` binaries in `PATH`.
- Keymap validation blocks duplicate bindings to avoid ambiguous action routing.
- CSV export intentionally works on currently displayed data for predictable terminal workflow and low overhead.
