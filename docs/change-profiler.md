# Change Profiler architecture

Change Profiler answers “what changed after this named point?” without changing the tracked database. The default engine is deliberately portable across PostgreSQL, MySQL/MariaDB, SQLite, Turso/libSQL, and Cloudflare D1.

## Portable exact mode

1. Preflight discovers tables, columns, stable row keys, and inexpensive size/row estimates.
2. The user reviews scope. Risky tables begin excluded; `A` explicitly includes the whole database.
3. Capture streams selected rows through a consistent source snapshot where the driver supports one.
4. Each normalized row receives a stable key hash and row hash. Exact before-values use adaptive per-row Zstandard compression in dbterm's private local SQLite store.
5. A scan streams current rows, looks up baseline hashes, and persists only inserted, updated, deleted, and schema-change records.
6. Finish removes the full baseline while preserving the compact report.

The loader reports phase, table position, row count, bytes processed, elapsed time, rate, percentage, and an ETA when a row estimate is available. PostgreSQL and MySQL estimates come from their catalogs. SQLite uses existing `ANALYZE` statistics when present, then a fast indexed rowid-range estimate; it does not add a full `COUNT(*)` pass.

Exact portable mode must still read every selected row at capture and scan time. Compression reduces local I/O and storage but cannot remove that source read.

## Why database logs are not the universal default

- PostgreSQL logical decoding requires logical WAL, replication privileges, and a persistent slot. An abandoned slot can retain WAL and consume server storage.
- MySQL/MariaDB change streaming requires accessible binary logs and a compatible row-based format.
- SQLite's session extension is disabled in default builds, requires declared primary keys, and observes only writes made through the attached SQLite connection.
- Turso/libSQL WAL replication is a replication interface, not a stable cross-product row-audit API exposed by dbterm's SQL connection.
- Cloudflare D1 Time Travel provides restore bookmarks, not a row-level change stream.

Native-log adapters are therefore an optional future acceleration mode. They must be capability-detected, explicitly enabled, monitored for retained-log growth, and cleaned up safely. Snapshot mode remains the compatibility fallback and the correctness oracle.

Official references:

- [PostgreSQL logical decoding](https://www.postgresql.org/docs/current/logicaldecoding.html)
- [MySQL binary log](https://dev.mysql.com/doc/refman/8.0/en/binary-log.html)
- [MariaDB row binlog events](https://mariadb.com/docs/server/server-management/server-monitoring-logs/binary-log/row-binlog-events)
- [SQLite session extension](https://sqlite.org/sessionintro.html)
- [Cloudflare D1 Time Travel](https://developers.cloudflare.com/d1/reference/time-travel/)
- [libSQL replication and incremental snapshots](https://github.com/tursodatabase/libsql/blob/main/docs/USER_GUIDE.md)
