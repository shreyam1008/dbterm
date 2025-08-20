# LazySQL Research + dbterm Improvement Roadmap (Lightweight-First)

Date: February 20, 2026  
Scope: Competitive analysis of `jorgerojas26/lazysql`, comparison with current `dbterm`, and a practical feature roadmap with strict lightweight constraints.

## 1) What LazySQL Does (Full Practical Breakdown)

LazySQL is a keyboard-first, terminal database manager focused on full table workflows (browse, filter, edit, inspect schema, export), not only query execution.

### Core product model

- Multi-connection TUI (connection picker + saved profiles).
- Vim-like navigation across tree/table/editor.
- SQL editor with execution and optional external editor handoff.
- Query history and saved queries per connection.
- Row/cell editing flows with pending changes and commit.
- Schema-introspection views (columns, constraints, foreign keys, indexes).
- CSV export for both table browsing and SQL results.
- JSON viewer for cell/row payloads.
- Custom keymap configuration.

### Database support

- MySQL
- PostgreSQL
- SQLite
- MSSQL (latest release line confirms support)

### Important architecture patterns worth noting

- Driver interface is extensive (`drivers/driver.go`) and includes:
  - metadata (`GetTableColumns`, `GetConstraints`, `GetForeignKeys`, `GetIndexes`)
  - browsing/paging (`GetRecords(...offset, limit...)`)
  - mutation execution (`ExecutePendingChanges`)
  - SQL execution (`ExecuteQuery`, `ExecuteDMLStatement`)
- Query history is persisted per connection (`internal/history/manager.go`).
- Saved queries are persisted per connection in TOML (`internal/saved/manager.go`).
- CSV export is streaming + atomic temp-file rename (`helpers/csv.go`), which is good for large datasets.
- Keymaps are grouped and configurable through config (`app/keymap.go` + README keymap section).
- Connection config supports pre-connect command hooks and variables (README `Commands` + `${port}` behavior).
- Connection-level read-only mode blocks mutation queries (README + `drivers/validation_test.go`).

### Recent upstream direction (validated from releases)

As of February 20, 2026, latest release is **v0.4.8 (February 17, 2026)**.  
Recent releases show ongoing investment in:

- Global search (`Ctrl+P`) and better navigation (`v0.4.8`).
- MSSQL support and query-history fixes (`v0.4.8`, `v0.4.7`).
- CSV export speed/memory fixes (`v0.4.8`).
- Custom keybindings and configurable query history limits (`v0.4.6`).

This indicates the product is converging on power-user workflows and long-session usability.

## 2) Current dbterm State (From Your Codebase)

`dbterm` already has strong foundations and some unique differentiators:

### Current strengths

- Single-binary workflow with lightweight runtime approach.
- Very fast, clean core layout (tables/query/results + dashboard).
- Multi-DB support including cloud targets:
  - PostgreSQL, MySQL, SQLite, Turso, Cloudflare D1.
- Nice operator features not present in LazySQL:
  - local service dashboard/control (`ui/services.go`)
  - backup modal for PG/MySQL (`ui/backup.go`)
- Good UX polish:
  - responsive layout handling
  - preview row limits
  - column zoom/width controls
  - sorting in result grid
  - row detail modal + clipboard copy

### Structural gaps vs LazySQL

- No persisted query history.
- No saved query library/snippets.
- No in-grid record editing/pending changes model.
- No schema metadata tabs (columns/constraints/FKs/indexes).
- No table-level filter input (`WHERE` composition flow).
- No true pagination model (offset/limit navigation).
- No built-in CSV export workflow.
- No per-connection read-only guardrails.
- No configurable keymap system.
- No global search across schema/query artifacts.
- No pre-connect command hook flow for tunnel/bastion workflows.
- No test suite in main `dbterm` code (currently zero `_test.go` files outside `_lazysql_ref`).

## 3) Feature Gap Matrix (What to Borrow, What to Adapt)

| Feature | LazySQL value | dbterm status | Recommendation |
|---|---|---|---|
| Query history (per connection) | High daily productivity | Missing | Add now (small-medium effort) |
| Saved queries/snippets | Reuse + team workflows | Missing | Add now (medium) |
| Read-only mode | Prevents accidental writes | Missing | Add now (small) |
| CSV export | Required in real workflows | Missing | Add now (medium) |
| Table filter input (`WHERE`) | Fast exploration | Missing | Add soon (medium) |
| Pagination | Scales to large datasets | Missing | Add soon (medium) |
| Schema metadata tabs | Better discoverability | Missing | Add soon (medium-high) |
| Inline edit + pending changes | Powerful CRUD in TUI | Missing | Add later (high) |
| Custom keymaps | Power-user retention | Missing | Add later (medium-high) |
| Global search (`Ctrl+P` style) | Fast object discovery | Missing | Add later (medium) |
| Pre-connect commands/tunnels | Infra-friendly | Missing | Add later (medium) |

## 4) Lightweight-First Product Strategy

To make `dbterm` a "lightest, super DB management app", adopt a strict rule:

- Add high-value workflows first.
- Keep dependency growth minimal.
- Avoid heavy abstractions until forced by scale.

### Suggested lightweight constraints

- Keep stripped Linux amd64 binary under a hard budget (example target: `<16 MB`).
- Add no heavy new UI framework.
- Prefer stdlib and existing deps first.
- For new storage, use tiny JSON/TOML files under `~/.config/dbterm`.
- Use lazy-loading for metadata and paged records.
- Consider build tags for optional cloud drivers if size becomes a blocker.

## 5) Prioritized Roadmap for dbterm

## Phase 1 (Highest ROI, 1-2 weeks)

1. Query history (per connection, capped list).
2. Saved queries/snippets (name + SQL).
3. Read-only connection flag that blocks mutation SQL.
4. CSV export from current results.

Why first: huge daily UX improvement, minimal architecture risk, low binary-cost increase.

## Phase 2 (Core data workflow, 2-3 weeks)

1. Table filter bar (WHERE clause helper).
2. Pagination controls (next/prev page, page size).
3. Metadata views: columns, constraints, foreign keys, indexes.

Why second: enables real data exploration at scale without forcing full edit complexity.

## Phase 3 (Power-user expansion, 3-5 weeks)

1. Inline row editing + pending-change queue + commit.
2. Custom keymap config (grouped commands).
3. Global search across tables/views/functions/query history.
4. Optional external editor handoff for SQL drafts.

Why third: high complexity and more moving parts; do after foundation is stable.

## 6) Concrete Implementation Notes for dbterm

These are suggested implementation anchors in your codebase.

### History + saved queries

- Add `internal/history` and `internal/saved` managers (similar persistence pattern as LazySQL, but keep API minimal).
- Add modal/page in `ui/` for:
  - Recent queries
  - Saved queries
  - insert query back into editor

### Read-only mode

- Extend `config.ConnectionConfig` with `ReadOnly bool`.
- In `ui/query.go`, detect mutation tokens and block when read-only.
- Show clear status indicator in status bar.

### CSV export

- Add export modal in `ui/` with path + scope.
- Stream writes (not full in-memory build).
- Reuse existing result rows first; later extend to "export all with pagination".

### Filter + pagination

- Add a small filter input control near results.
- Introduce offset/limit state in `ui/results.go`.
- Generate DB-specific paged queries via helper functions.

### Metadata tabs

- Extend `utils` with per-driver metadata SQL for:
  - columns
  - constraints
  - foreign keys
  - indexes
- Add results subview switching (records/columns/constraints/fks/indexes).

### Keymaps

- Introduce command constants + map resolution layer.
- Keep current defaults; allow optional override file only.

## 7) What Not to Copy 1:1

- Do not clone LazySQL's entire abstraction footprint immediately.
- Do not add all advanced workflows before pagination/filter/history basics.
- Do not increase binary size with large new dependencies for simple needs.

Use inspiration, not duplication.

## 8) Suggested Success Metrics

- Median "connect -> run -> export" workflow time reduced.
- Query re-run speed improves via history/saved queries usage.
- Large table interaction remains responsive due to pagination.
- Binary size remains within agreed budget after each phase.
- Crash-free sessions increase after introducing tests for new features.

## 9) Final Recommendation

Best near-term path:  
Build **history + saved queries + read-only + CSV export** first, then **filter/pagination/metadata tabs**.

This gives you most of LazySQL's high-value workflows while preserving `dbterm`'s core identity: fast, clean, lightweight, and operationally practical.

## 10) Sources (Web + Code)

- LazySQL repo: https://github.com/jorgerojas26/lazysql
- LazySQL README (features/config/usage/keybindings): https://raw.githubusercontent.com/jorgerojas26/lazysql/main/README.md
- LazySQL release v0.4.8 (Feb 17, 2026): https://github.com/jorgerojas26/lazysql/releases/tag/v0.4.8
- LazySQL release v0.4.7 (Jan 25, 2026): https://github.com/jorgerojas26/lazysql/releases/tag/v0.4.7
- LazySQL release v0.4.6 (Jan 2, 2026): https://github.com/jorgerojas26/lazysql/releases/tag/v0.4.6
- Your local `dbterm` code inspected in:
  - `README.md`
  - `ui/app.go`
  - `ui/query.go`
  - `ui/results.go`
  - `ui/tables.go`
  - `ui/connect.go`
  - `ui/dashboard.go`
  - `ui/services.go`
  - `ui/backup.go`
  - `config/config.go`
  - `utils/db.go`
