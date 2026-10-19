# Change Profiler checkpoints

A checkpoint answers **“what changed between Start and Stop?”** Open `Alt+W`, press `N`, choose a capture mode, review the tables, then start. Perform your operation and press `F` to stop and inspect the net diff. The saved report contains inserted, updated and deleted rows with exact before/after values. Changes that cancel out disappear; a primary-key move appears as a deletion and insertion. Rolled-back writes are excluded.

## Incremental journal

This mode installs private journal tables and triggers after an explicit confirmation. Start reads metadata, not existing table contents. Committed writes from all connections to the selected tables record their row images in the same transaction as the write. Stop freezes the journal, reduces its events and saves a local report before removing the source objects. SQLite conflict handling also uses indexed lookups for affected identities at Stop.

Work and storage depend on writes during the checkpoint and the size of changed values, rather than the total number of existing rows. A write-heavy checkpoint can still produce a large journal: updates store complete row images, and reduction holds changed identities in memory. Keep checkpoints short and stop or discard them when finished. This is a temporary diagnostic recorder, not a backup or an indefinite audit service.

| Engine | Capture and continuity | Limits |
| --- | --- | --- |
| SQLite | Transactional row triggers; writer transactions establish Start/Stop boundaries. Schema version and exact source objects are checked. Recording survives closing dbterm. | Requires stable indexed keys. Unsupported virtual tables and partial/expression/custom-collation unique indexes are rejected. Schema changes invalidate coverage. Replacement conflicts are handled even with recursive triggers disabled. |
| Turso/libSQL | Uses the SQLite SQL-journal route, subject to server capabilities and permissions. | Hosted Turso was not exercised in this review; Start must succeed with the actual service. |
| PostgreSQL | Transactional row triggers and a gate drain in-flight writers at Start/Stop. A retained connection holds `SHARE UPDATE EXCLUSIVE` locks to prevent capture gaps from schema/trigger changes. | Keep dbterm's connection open. Requires at least two pool connections. Locks block DDL, `TRUNCATE`, and autovacuum/ANALYZE on tracked tables until Stop/discard. Repeatable-read/serializable writers may need transaction retries at boundaries. Partitions, inheritance, RLS and keyless tables are rejected. |
| MySQL | Transactional row triggers and a gate drain in-flight writers; a retained metadata transaction protects table/trigger definitions. | Keep dbterm's connection open; at least two pool connections are needed. InnoDB only. DDL/`TRUNCATE` wait until Stop/discard. Cascading foreign-key writes are rejected because MySQL does not fire row triggers for them. Tested against official MySQL 8.4; MariaDB compatibility is not established. |
| Cloudflare D1 | Snapshot comparison only. | No supported incremental SQL-journal adapter. |

Installation permissions are engine-specific: SQLite needs writable schema access, PostgreSQL needs schema/table/trigger/function privileges, and MySQL needs table/trigger privileges and any server-required routine permissions. Installation failure is visible; dbterm does not silently switch to a full scan. A read-only connection defaults to snapshot mode.

These are SQL change journals, not PostgreSQL WAL consumers or MySQL binlog consumers. dbterm does not enable replication, create replication slots, or change server logging configuration. The source's transactional guarantees and ordinary privileged-administrator trust still apply; a database administrator can alter the database or terminate guards. Missing continuity evidence causes Stop to refuse a complete report. PostgreSQL/MySQL checkpoints cannot resume guaranteed recording after a process or guard-connection loss; discard their retained source journal and start a new checkpoint.

## Recovery and source ownership

Source object identities are saved locally before installation. Each recorder verifies its reserved namespace and source-object ownership before cleanup. Stop saves the report before deleting source journal objects; a failed cleanup leaves the report and a visible **source journal retained** state. Press `F` to retry Stop/cleanup. Press `D` to discard a journal and delete its report; this needs the original database connection. Local recovery metadata is retained until source cleanup succeeds.

Finish or discard checkpoints and complete source cleanup before taking a dbterm backup. Backup capture refuses journal objects in the source or captured artifact, since restoring active recorder triggers would create an orphan journal. PostgreSQL backup validation also needs the matching `pg_restore` client.

Do not manually delete individual journal objects: orphaned triggers can make application writes fail. Do not delete dbterm's local profile while it owns a journal. Private objects use the `__dbterm_cp_…` namespace on SQLite, `_dbterm_cp_…` on MySQL, and a `dbterm_cp_…` schema on PostgreSQL; dbterm hides these exact reserved names from its normal table list. Journal contents include changed row data and inherit source database storage/access rules. The local report also contains row data in dbterm's private profiler store.

The report is the authoritative journal diff. Ordinary Results-cell highlighting is disabled for journal reports because driver-returned value representations can differ from journal encodings; table change counts remain visible. Writer identity remains **Unknown** without a separate database audit trail.

## Snapshot comparison

Choose **Snapshot comparison (full reads)** for read-only connections, unsupported schemas/engines, or schema-change comparisons. This mode installs no source objects and remains compatible with existing saved anchors.

1. Preflight discovers tables, stable keys and inexpensive size/row estimates. Risky tables begin excluded; `A` explicitly includes the whole database.
2. Capture streams the selected tables into the private local store. Stable key/row hashes and adaptively compressed exact before-values form the baseline.
3. `S` reads selected tables again and updates the report. `F` performs a final comparison and removes the full baseline while preserving the report.

PostgreSQL/MySQL use a consistent repeatable-read snapshot, SQLite/Turso use a transaction, and D1 is best-effort. Capture and every comparison still read all selected rows. Progress shows phase, table, rows, bytes, elapsed time and estimates when available. SQLite estimates use existing statistics or indexed rowid ranges rather than a preliminary full `COUNT(*)`.

An older SQLite anchor that used a nullable declared primary key is marked failed while preserving its baseline and last report. Start a new anchor with corrected row identity; deleting the old anchor later removes its local data.

## Browsing the report

Reports page through 200 changes at a time with `[` and `]`. `Tab` switches table/row focus; `Enter` opens row details, and `Enter` on a value opens a bounded viewer. `C` copies that complete value. NULL, empty text and absent columns have distinct labels. See the [synthetic-data review](reviews/scalability-and-tui.md) for test scope, timings and remaining rendering limitations.

## Future native-log adapters

Native replication feeds could avoid source triggers and retained schema guards, but require separate capability checks and lifecycle management:

- [PostgreSQL logical decoding](https://www.postgresql.org/docs/current/logicaldecoding.html) needs logical WAL, replication privileges and slot-retention management.
- [MySQL binary logs](https://dev.mysql.com/doc/refman/8.0/en/binary-log.html) need accessible row logs, durable cursors and gap handling.
- [SQLite's session extension](https://sqlite.org/sessionintro.html) observes only writes on its attached connection and is not enabled in default builds.
- [D1 Time Travel](https://developers.cloudflare.com/d1/reference/time-travel/) supplies restore bookmarks rather than row-change events.

Snapshot comparison remains the compatibility fallback and a correctness oracle for incremental diff tests.
