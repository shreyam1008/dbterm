# dbterm: Go database TUI and backup agent

dbterm is a keyboard-first database workbench created by [Shreyam Adhikari](https://shreyam1008.com.np/) (`shreyam1008`). The canonical product site is [dbterm.shreyam1008.com.np](https://dbterm.shreyam1008.com.np/) and the MIT-licensed source is on [GitHub](https://github.com/shreyam1008/dbterm).

## Useful daily database work

- Discover PostgreSQL and MySQL/MariaDB databases available to one saved server login.
- Inspect tables and rows without opening a heavyweight graphical client.
- Sort, refresh and apply typed filters.
- Copy identifiers and follow declared relationships in either direction across table chains.
- Run SQL and use import, export and local-service workflows.
- Work with PostgreSQL, MySQL/MariaDB, SQLite, Turso/LibSQL and Cloudflare D1.

## Local access for AI agents

`dbterm mcp serve` starts a local STDIO Model Context Protocol server. An MCP-capable agent can list allowed saved profiles, inspect schemas and tables, run bounded read-only SQL, explain a query, and follow one record across declared foreign keys. Saved passwords and tokens are never returned.

Access defaults to the active connection. Database mutation is not exposed. Creating or updating a connection profile is a separate Settings opt-in because profiles can contain credentials.

## Verified backup routes

dbterm separates the database source from the artifact destination. It supports local-to-local, local-to-remote, remote-to-local and remote-to-remote backups. Destinations may be absolute local folders, mounted volumes or configured `rclone://remote/path` locations.

The backup pipeline stages an engine-native dump or snapshot, verifies it, applies optional gzip/ZIP/zstd compression and age X25519 encryption, calculates SHA-256 and publishes without overwriting an existing artifact. Jobs can run through systemd, launchd or Windows Task Scheduler.

Backup covers PostgreSQL, MySQL/MariaDB, SQLite, Turso and Cloudflare D1. Restore currently covers PostgreSQL, MySQL/MariaDB and local SQLite.

## Read more

- [Feature map](https://dbterm.shreyam1008.com.np/features/)
- [Complete user guide](https://dbterm.shreyam1008.com.np/guide/)
- [Backup Center](https://dbterm.shreyam1008.com.np/backup/)
- [AI agent and MCP guide](https://dbterm.shreyam1008.com.np/agents/)
- [Source code](https://github.com/shreyam1008/dbterm)
- [Go package](https://pkg.go.dev/github.com/shreyam1008/dbterm)

## Machine-readable documentation

- [Complete user guide (Markdown)](https://dbterm.shreyam1008.com.np/guide.md)
- [Backup and restore handbook (Markdown)](https://dbterm.shreyam1008.com.np/backup.md)
- [AI agent and MCP guide (Markdown)](https://dbterm.shreyam1008.com.np/agents.md)
- [Change Profiler guide (Markdown)](https://dbterm.shreyam1008.com.np/change-profiler.md)
- [Full documentation corpus](https://dbterm.shreyam1008.com.np/llms-full.txt)
- [Structured product metadata (JSON)](https://dbterm.shreyam1008.com.np/product.json)
