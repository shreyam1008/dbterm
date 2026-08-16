# dbterm: Go database TUI and backup agent

dbterm is a keyboard-first database workbench created by [Shreyam Adhikari](https://shreyam1008.com.np/) (`shreyam1008`). The canonical product site is [dbterm.shreyam1008.com.np](https://dbterm.shreyam1008.com.np/) and the MIT-licensed source is on [GitHub](https://github.com/shreyam1008/dbterm).

## Useful daily database work

- Discover PostgreSQL and MySQL/MariaDB databases available to one saved server login.
- Inspect tables and rows without opening a heavyweight graphical client.
- Sort, refresh and apply typed filters.
- Copy identifiers and follow primary or foreign-key relationships between tables.
- Run SQL and use import, export and local-service workflows.
- Work with PostgreSQL, MySQL/MariaDB, SQLite, Turso/LibSQL and Cloudflare D1.

## Verified backup routes

dbterm separates the database source from the artifact destination. It supports local-to-local, local-to-remote, remote-to-local and remote-to-remote backups. Destinations may be absolute local folders, mounted volumes or configured `rclone://remote/path` locations.

The backup pipeline stages an engine-native dump or snapshot, verifies it, applies optional gzip/ZIP/zstd compression and age X25519 encryption, calculates SHA-256 and publishes without overwriting an existing artifact. Jobs can run through systemd, launchd or Windows Task Scheduler.

Backup covers PostgreSQL, MySQL/MariaDB, SQLite, Turso and Cloudflare D1. Restore currently covers PostgreSQL, MySQL/MariaDB and local SQLite.

## Read more

- [Feature map](https://dbterm.shreyam1008.com.np/features/)
- [Product guide](https://dbterm.shreyam1008.com.np/guide/)
- [Backup Center](https://dbterm.shreyam1008.com.np/backup/)
- [Source code](https://github.com/shreyam1008/dbterm)
- [Go package](https://pkg.go.dev/github.com/shreyam1008/dbterm)
