# Scalability and terminal component review

This review uses synthetic data and compares against commit
`17b33b429b1e088baf7b9541a3ba605e3fea4f47`. No user databases were accessed.
It covers checkpoint internals, result-table components, logical backup fidelity,
and restore cancellation. It is **not a completed end-to-end UX audit**: the
running cloud environment denies downloads of pinned SQLite/compression modules,
so the complete application, native MySQL/PostgreSQL integrations, and full
checkpoint/backup workflow benchmarks have not run.

## What was exercised

Run `python3 scripts/review-fixtures.py --million` from the repository root.
The standard-library generator refuses to overwrite an existing output folder.
It creates an isolated connection profile under `.local/review/fixtures/config`.
Set `DBTERM_CONFIG_DIR` to that absolute path and use separate absolute
`DBTERM_STATE_DIR` and `DBTERM_LOG_DIR` directories when launching dbterm.

| Fixture | Rows in main table | File size | Purpose |
| --- | ---: | ---: | --- |
| Empty | 0 | 24 KB | Empty states |
| Small | 100 | 90 KB | Mixed values and everyday navigation |
| Medium | 10,000 | 6.9 MB | Paging and ordinary checkpoints |
| Large | 100,000 | 68.5 MB | Scan scaling |
| Million | 1,000,000 | 684.4 MB | Large local database |
| Catalog | 100, plus 200 tables | 934 KB | Long object lists |
| Wide | 1,000, plus a 150-column table | 22.2 MB | Wide results and long cells |

Fixtures contain Unicode/Indic/emoji, combining characters, literal color tags,
NULLs, empty strings, embedded NULs, multiline text, dates, JSON, BLOBs, composite
keys, keyless duplicates, and identifiers requiring quoting. Python SQLite
validated row counts, integrity, and representative stored values in all seven.
Database generation and integrity checks do not establish application readiness.

## Findings and changes

1. **Cell previews processed far more text than they displayed.** Producing a
   100-character preview repeatedly segmented the entire string and allocated
   storage for every grapheme. Preview construction now examines UTF-8 prefixes
   until it has sufficient complete grapheme boundaries. Full copy/export values
   remain separate. Indic conjuncts, ZWJ emoji, flags, long combining sequences,
   and malformed UTF-8 have regression coverage.
2. **Binary values were not readable.** The real table component rendered raw
   BLOB bytes as control/replacement characters; an empty BLOB looked like empty
   text. The revised display identifies binary values by size and hex preview.
3. **Large checkpoint reports were inaccessible beyond a fixed cap.** Reports
   now load 200 changes per page, show their position and total, and support
   `[`/`]` for previous/next pages. `Tab`/`Shift+Tab` switch panels. Row keys are
   displayed as key/value pairs instead of internal JSON metadata. Row details
   distinguish SQL NULL, empty values, and absent columns, escape literal markup,
   retain full cell values, and restore focus on return.
   On narrow terminals the table selector stacks above the report. Before/after
   columns share the available detail width and selection has an explicit
   foreground/background pair. Large cell values use bounded byte/grapheme
   windows with previous/next controls and full-value copy, rather than passing
   a multi-megabyte string into the text renderer.
4. **Highlights loaded only an initial slice of all changes.** Visible rows now
   use indexed key lookups, independent of their position in the saved report.
5. **Checkpoint preparation and comparison did unnecessary work.** SQLite
   boundary estimates use separate indexed MIN/MAX subqueries. Row normalization
   is shared with key encoding, and unchanged rows do not load saved value blobs.
6. **Repeated checkpoint scans could change their interpretation of the
   baseline.** Report-only metadata is now distinguished from captured tables.
   Deleting an anchor also removes its saved changes, schema events, activity,
   and temporary seen-row records in one transaction. Nullable SQLite primary
   keys, shadowed rowids, and literal dots in identifiers receive regression
   coverage. Older reports that already lost creation events cannot always have
   their original provenance reconstructed.
   Legacy SQLite anchors with a formerly assumed non-null primary key now
   require a fresh baseline explicitly; they preserve the old report instead
   of reporting every unchanged row as a deletion and insertion. The old anchor
   is marked failed, freeing its active-target slot for a replacement.
7. **Turso logical backups could change data on restore.** Embedded-NUL text,
   deleted AUTOINCREMENT high-water marks, and valid user objects beginning with
   `sqlite` without the reserved underscore are now preserved. User-table
   discovery uses the same literal reserved-prefix rule.
8. **Large SQL safety scans ignored cancellation.** PostgreSQL, MySQL, and SQLite
   restore scanners now check the restore context during streaming reads,
   including inside long literals, without weakening their safety checks.
9. **Local-service copy misled Docker users.** The panel now describes local
   service detection and directs Docker users to connect through published host
   ports. It does not claim to discover or control containers.

## Measured results and their limits

Measurements are from isolated helper/component harnesses on the review machine,
using the exact production functions. They are not end-to-end timings or service
level guarantees. Large input creation is excluded from timed preview loops.
The baseline preview used a single measured operation; updated previews used
100 ms benchmark runs, so ratios are indicative rather than statistically
controlled comparisons.

| Check | Baseline | Updated | Scope |
| --- | ---: | ---: | --- |
| 1 KiB ASCII preview | 2.005 s, 43.4 MB, 28,134 allocations | 0.271 ms, about 10 KB, 10 allocations | Preview helper |
| 1 MiB ASCII preview | Not run to completion | About 0.290 ms | Preview helper |
| 4 MiB ASCII preview | Not run to completion | About 0.366 ms | Preview helper |
| Primary-key encoding, 64 KiB row | 509.5 μs | 115.7 μs | Normalization/encoding; excludes compression/store |
| SQLite estimate, 1M-row/513 MB fixture | 117.5 ms | 0.004 ms | Warm query minimum, five samples |

The exact-source checkpoint component harness also caught a large-cell viewer
stall: drawing about 1.5 MB as one TextView was still running after 85 seconds.
The bounded cell viewer completes the same component traversal and exposes all
bytes through paging/full copy. This remains component testing with a fixture
store, not native database or full-application execution.

The separate real tview/tcell redraw benchmark found a **remaining bottleneck**:
at 120×36, a static 100×8 table with the default 30-cell column cap took about
20 ms for 16-character cells and 390 ms for 100-character cells. The local Unicode
compatibility layer recomputes boundary information while drawing. The preview
fix does not solve this redraw cost. It needs a focused Unicode/layout change
with broad boundary-conformance tests; replacing Indic-aware layout casually
would regress existing language support.
The grid's estimated byte budget also remains an estimate: retaining lossless
values means a few very large cells can exceed it. This patch does not claim a
hard memory ceiling for arbitrary result pages.

Executed checks include exact-source helper compatibility tests, standalone SQL
safety tests with race detection, Python SQLite round trips for logical-export
expressions, and production store migration/deletion SQL. Added full-package
regressions and workflow benchmarks remain unexecuted until module downloads work.
Do not treat blocked test packages as passing or count helper stubs as full-app
execution.

## Visual evidence and flow status

![Result-table component before binary preview refinement](assets/result-grid-before.png)

![Result-table component after binary preview refinement](assets/result-grid-after.png)

![Checkpoint report at 80×24](assets/checkpoint-report-80x24.png)

![Balanced before/after details at 80×24](assets/checkpoint-detail-80x24.png)

![Bounded view of a large checkpoint cell](assets/checkpoint-cell-120x32.png)

This is a current tcell simulation-screen capture of the production result-table
component with a synthetic SQL driver. It is not an OS-terminal screenshot or a
full application session. The rasterizer lacks some Indic/CJK/emoji glyphs; its
cell data retains the actual runes, so font fallback cannot be assessed here.
Checkpoint images use exact production UI methods with a controlled fixture
store and application shell. That harness passed page traversal, focus switching,
literal data, full-copy, and return-navigation checks at 80×24 and 120×32. Its
fixture store does not validate native database capture or the persisted diff
implementation. A focused large-cell TextView draw measured about 30–31 ms;
drawing its underlying report/detail layers as well took about 122–193 ms.

| Step | Status | Evidence or remaining work |
| --- | --- | --- |
| 1. Generate and validate varied databases | Passed | Seven fixtures, including 1M rows |
| 2. Inspect mixed-value result table | Component reviewed | 80×24 and 120×28 cell captures; binary ambiguity confirmed |
| 3. Create and scan an anchor | Full flow blocked | Helper/SQL evidence only; full package needs pinned modules |
| 4. Browse checkpoint report and row details | Component checks passed; full flow pending | Exact-source fixture-store keyboard tests and captures; native-store regressions still blocked |
| 5. Run, inspect, and restore a backup | Full flow blocked | SQL-expression round trips and safety scanner tests only |
| 6. Test native/cloud database integrations | Unrun | MySQL/PostgreSQL/Turso/D1 services were not exercised |

Keyboard and contrast checks on these components do not establish screen-reader
support or accessibility compliance. Color-only NULL distinctions in ordinary
result grids, control-character visibility, and Unicode font fallback still
need attention during a live-terminal pass.

## How checkpoints and backup logs differ

The current Change Profiler captures selected tables into a local baseline and
compares later full reads against that baseline. It is not a consumer of database
transaction logs. Matching rows is faster after these changes, but exact capture
and comparison still read every selected row; keyless duplicate tracking also
retains a map proportional to the distinct rows.

Recorded dbterm write activity is contextual evidence, not an audit trail of all
writers. Backup activity records runs and attempts. Agent logs are a bounded
global diagnostic tail. None of these is PostgreSQL WAL or the MySQL binlog, and
artifact verification is not proof that a recovery rehearsal succeeded.

The existing architecture already identifies opt-in native-log adapters as a
future acceleration path. Such adapters need a consistent initial snapshot,
durable cursors, gap detection, transaction handling, schema-change handling,
and explicit ownership of replication resources. PostgreSQL slots can retain
WAL; MySQL needs appropriate row logging and retained binlogs. SQLite WAL is
not a portable semantic change feed. None of this patch consumes those logs.

## One feature proposal: recovery rehearsal receipts

Add a **Rehearse Restore** action to an existing backup artifact. Restore it into
a new disposable target, run integrity checks plus optional user-chosen
verification queries, and record a durable receipt. Keep this separate from the
artifact's existing checksum/format verification: a readable artifact and a
tested recovery are different evidence.

The receipt should identify the artifact and SHA-256, engine, disposable target,
start/end time, restore result, verification results, and cleanup result. Backup
Center can then show **Last artifact verified** and **Last recovery rehearsed**
as separate facts, with a details page that explains failures. Store summaries
and outcomes rather than row contents or credentials.

Start with local SQLite, which permits a truly isolated temporary target without
a separate service. Use the existing guarded restore pipeline and always require
a new target; never select a production connection automatically. Keep a failed
rehearsal's diagnostics and let the user choose whether to retain its target for
inspection. PostgreSQL/MySQL support can follow with explicitly configured
disposable databases and permissions.

Acceptance should cover successful mixed-type recovery, a corrupt/truncated
artifact, wrong encryption identity, cancellation, failed verification queries,
cleanup failure, and restart persistence. A green rehearsal must mean the restore
and its selected checks actually completed, with the artifact checksum tied to
the saved result. This proposal is documented, not implemented in this patch.

## Finishing the live review

Apply the saved `storage.googleapis.com` and `api.github.com` network settings to
the running environment. The former unblocks pinned modules; the latter is
needed for PR API access. Git read access alone does not establish PR permission.
Then run:

```sh
export PATH=/workspace/.tools/go/bin:$PATH
export GOMODCACHE=/workspace/.cache/go-mod GOCACHE=/workspace/.cache/go-build
export GOFLAGS=-mod=readonly GOMAXPROCS=4
go mod download
go mod verify
make build
go test ./...
go vet ./...
go test -race ./...
DBTERM_FORM_SNAPSHOTS="$PWD/.local/review/screens" go test ./internal/ui -run 'TestProfilerReport|TestProfilerDiffDetail|TestBackupSmallTerminal'
go test ./internal/ui -run '^$' -bench 'BenchmarkResultTextPreview|BenchmarkResultTableDraw' -benchmem
go test ./internal/changeprofiler -run '^$' -bench . -benchmem -benchtime=1x
```

Finally use the isolated profile in a real terminal, exercise every report page,
cancel a long capture/restore, restore into a disposable target, compare data,
and capture the dashboard, preflight, progress, report, details, backup outcomes,
and recovery navigation before calling the full UX review complete.
