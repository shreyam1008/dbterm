# dbterm complete user guide

## Start here

dbterm is a keyboard-first terminal database workbench for PostgreSQL, MySQL/MariaDB, SQLite, Turso/LibSQL, and Cloudflare D1. It combines saved connections, server-level database discovery, schema and data browsing, SQL, relationship navigation, change comparison, local service controls, import/export, and verified backups in one binary.

The normal first session is:

1. Install dbterm and run `dbterm`.
2. Press `N` on the Dashboard.
3. Choose a database type, enter the connection details, and select **Save & Connect**.
4. For PostgreSQL or MySQL, leave **Default Database** empty if you want dbterm to show every database visible to that server login.
5. Select a table, use `Alt+Q` to focus Query, and press `Ctrl+Space` for local suggestions and safe selected-table templates.
6. Press `Alt+H` at any time for the offline Guide & SQL Reference.

dbterm is local software. It connects directly from your computer to your databases; it does not send connection data through a hosted dbterm control plane.

### Database capability matrix

| Database | Query and schema | Backup | Restore | Local service controls |
| --- | --- | --- | --- | --- |
| PostgreSQL | Yes | Custom `pg_dump` archive | Yes, content-detected | Yes |
| MySQL / MariaDB | Yes | Single-database `mysqldump` SQL | Yes, content-detected | Yes |
| SQLite | Yes | Consistent built-in snapshot | Snapshot or streamed SQL | No service required |
| Turso / LibSQL | Yes | Transaction-backed logical SQL | Inspectable only in this release | No |
| Cloudflare D1 | Yes | Cloudflare native SQL export | Inspectable only in this release | No |

## Install, verify, update, and uninstall

Release binaries are available for Linux, macOS, and Windows on amd64 and arm64. The direct installers download the matching release artifact and verify it against the published checksums.

### Linux and macOS installer

```bash
curl -fsSL https://raw.githubusercontent.com/shreyam1008/dbterm/main/install.sh | bash
```

### Windows PowerShell installer

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/shreyam1008/dbterm/main/install.ps1 | iex"
```

### Go toolchain

```bash
go install github.com/shreyam1008/dbterm/cmd/dbterm@latest
```

### Homebrew tap

```bash
brew tap shreyam1008/tap
brew install shreyam1008/tap/dbterm
```

### Scoop bucket

```powershell
scoop bucket add shreyam1008 https://github.com/shreyam1008/scoop-bucket
scoop install dbterm
```

Homebrew and Scoop can lag behind the newest GitHub release. Check the version shown by the package manifest before choosing a channel. WinGet is not documented as an install option until its public package is merged. The published APT metadata is not yet signed, so use the direct `.deb` release artifact or the verified shell installer instead of treating that repository as authenticated.

Verify the installed build and resolved paths:

```bash
dbterm --version
dbterm --info
```

Update to the newest release or a specific version:

```bash
dbterm --update
dbterm --update 0.10.1
```

The Dashboard `U` action opens the same Version & Update workflow. Updates verify the release checksum and replace only the executable. Saved connections, settings, query history, backup plans and history, completed artifacts, and Change Profiler anchors remain in the invoking user's profile. If a system-owned binary needs elevation, `sudo dbterm --update` preserves the invoking user's data rather than switching to a root profile.

Uninstall options:

```bash
dbterm --uninstall
dbterm --uninstall --yes
dbterm --uninstall --purge
```

Normal uninstall removes the binary. `--purge` also removes dbterm-owned config, state, and logs, but never removes backup artifacts written to a destination you chose. Purge refuses to remove a discovered private age identity or backup-like file from dbterm's directories; move the recovery key somewhere safe or delete it explicitly before retrying. If a package manager owns the binary, uninstall through that package manager.

## Create and manage connections

Press `N` on the Dashboard to open the connection form. Every connection has a display name, database type, and optional **Read-Only Guard** setting. This is a convenience check in the Query workspace, not a database security boundary: it inspects only the statement's first SQL token and blocks obvious write-leading statements such as `INSERT`, `UPDATE`, `DELETE`, and `CREATE`.

Statements beginning with `WITH`, `EXPLAIN`, or `PRAGMA` are classified as readable by this guard even though a data-changing CTE, `EXPLAIN ANALYZE` of a write, or a writable SQLite pragma can have side effects. For enforced protection, connect with database credentials or grants that cannot write; where applicable, also use the engine's read-only mode or a read-only filesystem. Keep the guard enabled as an extra warning, not as the only control.

### PostgreSQL

Provide either a PostgreSQL connection string or the structured fields:

- Host and port; the default port is `5432`.
- Database user and password.
- Optional default database.
- PostgreSQL SSL mode when required by the server.

The form can parse URL and key/value PostgreSQL DSNs. Leave the database empty to save a server-level login and browse all accessible databases.

### MySQL and MariaDB

Provide either a MySQL DSN or the structured fields:

- Host and port; the default port is `3306`.
- Database user and password.
- Optional default database.

Ubuntu's MySQL `root` account often uses socket authentication and may not work over TCP. Create or use a TCP-capable MySQL account; the Linux sudo password is not a database password.

### SQLite

Provide the path to the local SQLite database file. SQLite does not need a username, password, port, or local service manager.

### Turso / LibSQL

Provide the database URL, such as `libsql://database-owner.turso.io`, and its auth token.

### Cloudflare D1

Provide the Cloudflare account ID, D1 database ID, and API token. dbterm uses the D1 API for queries and native exports.

### Connection form actions

- **Save & Connect** stores the profile, tests the selected database when appropriate, and opens it. A server-level PostgreSQL/MySQL profile opens the database picker.
- **Save Only** stores the profile without leaving the Dashboard.
- **Find DBs** lists databases visible to the entered PostgreSQL/MySQL login.
- **Test** checks the current fields without saving them.
- **Parse DSN** copies supported PostgreSQL/MySQL connection-string values into the structured fields so they can be reviewed.
- `Esc` cancels without saving.

Connection secrets are stored in a private per-user `connections.json` file so unattended jobs can reuse them. They are not stored in an OS keyring. Protect the OS account and dbterm directories, use narrowly privileged database accounts, and never paste credentials into issue reports.

## Dashboard and database discovery

The Dashboard owns saved logins, health checks, update access, and entry points into the major operational pages.

| Key | Dashboard action |
| --- | --- |
| `Enter` | Connect to the highlighted profile or its default database |
| `A` | Browse every database visible through the highlighted PostgreSQL/MySQL server login |
| `N` / `E` / `D` | New, edit, or delete a connection |
| `R` | Run fresh connection health checks |
| `Ctrl+B` | Create a scheduled backup plan with the highlighted connection preselected |
| `B` | Open Backup Center |
| `I` | Import SQL into the highlighted PostgreSQL/MySQL connection |
| `G` | Open Settings |
| `H` | Open the offline guide |
| `S` | Open local database services |
| `U` | Open Version & Update |
| `W` or `Esc` | Return to an already-open workspace |
| `Q` | Quit |
| `1`–`9`, `0` | Select one of the first ten saved connections |

Inside the server database picker, `Enter` opens a database temporarily with the original login, `D` makes it the optional default, and `N` saves a separate connection. Separate saved connections are useful when a backup job needs one specific database.

Dashboard health checks can run automatically or only when `R` is pressed. Change this in Settings if startup checks are slow on a network with unreachable servers.

## Navigate the workspace and command palette

The workspace contains Tables, Query, and Results. Direct focus shortcuts and forward/backward cycling preserve each panel's current table, row, column, scroll position, and type-ahead text.

- `Alt+T`, `Alt+Q`, and `Alt+R` focus Tables, Query, and Results.
- `Tab` and `Shift+Tab` cycle the three panels.
- `Alt+F` toggles fullscreen Results.
- `Alt+D` returns to the Dashboard.
- `Esc` closes a completion or active filter first, then moves out of Query or returns to the Dashboard.
- `Ctrl+C` cancels an active query, import, export, or cancellable loader before it quits the app.

`Ctrl+P` opens the command palette. It searches documented actions, tables, collapsed columns, views, functions, procedures, triggers, recent successful SQL, backup jobs, and other database objects. Use Up/Down to select, `Enter` to open, and `Esc` to close. Palette searches are local; they do not query the database on every keystroke.

## Browse tables, columns, and database objects

The Tables panel is a searchable schema tree rather than a flat table list.

- Type to search tables, views, and other database objects without mixing in columns. Up/Down cycles through matches; Enter opens the highlighted item.
- On wide terminal layouts, drag the Tables panel's right border to resize the sidebar.
- Right expands a table and then enters its children. Left returns to the parent and then collapses it.
- Clicking `▸` or `▾` expands or collapses without opening rows.
- `Enter` opens a table; on a child column it opens the table and selects that Results header.
- `Space` pins or unpins a table at the top for the current database connection.
- `Shift+C` or right-click copies the complete table or column name.
- Backspace edits the active search; `Esc` clears it, then returns to the Dashboard on a second press.
- `Alt+M` opens the selected table's columns, primary keys, foreign keys, and indexes.

Expanded columns show `PK`, `FK`, `NN`, and data-type metadata. Names appear first; additional metadata loads asynchronously so large schemas remain responsive. Only one branch stays expanded at a time.

Views can be opened as row data. Functions, procedures, triggers, and extensions open read-only definition/details pages when the engine exposes them.

## Write SQL, use autocomplete, and query history

In Query, `Enter` executes the current SQL and `Shift+Enter` or `Alt+Enter` inserts a newline. Queries have a 30-second execution timeout. Press `Esc` or `Ctrl+C` to request cancellation; dbterm keeps the interface responsive while the driver finishes the safe cancellation path.

`Ctrl+Space` opens local SQL suggestions. Up/Down or `Ctrl+P`/`Ctrl+N` changes the selection; `Tab` or `Enter` inserts it; `Esc` dismisses suggestions. Completions include:

- SQL keywords, clauses, functions, and routines.
- Live tables, views, columns, schemas, and databases from the local catalog.
- Alias-aware columns and relation-context ranking.
- Typo-tolerant table and keyword recovery.
- Ready read-only templates for the selected table: preview, row count, named-column, newest-row, and useful grouped summaries when matching columns exist.
- Safe next clauses after a complete table name, including limits, recent-first ordering, and non-NULL filters.

Metadata is refreshed away from the typing path, so opening or accepting a suggestion never performs a network or database query.

Successful queries are stored per connection. `Alt+Y` opens newest-first history; `Enter` loads one into Query and `Esc` or Backspace closes the list. Failed and canceled statements are not presented as successful history.

The full query editor can execute writes. A saved profile's **Read-Only Guard** blocks only obvious write-leading tokens; `WITH`, `EXPLAIN`, and `PRAGMA` are bypass classes and may still change data. Review destructive SQL carefully and use database-enforced read-only credentials or grants whenever writes must be impossible.

## Work with result rows and columns

Table browsing uses bounded server-side pages. Ad-hoc query results are also safety-limited for terminal rendering.

### Navigate, find, and inspect

- Arrow keys move through cells.
- From the first data row, press Up to enter the selectable header row. Type a column name to jump, Left/Right to move, and Down or `Enter` to return to that column's data.
- `Shift+C` on a header copies the complete column name.
- `C` on data copies the complete cell value even when its display preview is shortened.
- `Enter` opens a vertical row-detail view with every complete value; use arrows and `C` inside it.
- `Space` toggles the current row selection. `Alt+A` selects every displayed row and `Alt+C` clears the selection.

### Filter

Press `/` to build typed predicates. Supported operators are `=`, `!=`, `>`, `>=`, `<`, `<=`, contains, starts-with, `IS NULL`, and `IS NOT NULL`. **Add AND** composes predicates; Apply updates the query; Remove Last and Clear All back out safely. Tab and Shift+Tab move between form fields.

Press `V` to apply or update equality for the selected column from the clipboard or the last cell copied inside dbterm. A real SQL `NULL` becomes `IS NULL`; the text `"NULL"` remains text. Table filters are remembered for the active connection and table during the session. The first `Esc` clears filters and resets to the first page; the next returns to the Dashboard.

### Follow relationships and values

Press `F` on a key cell to see declared outgoing parent and incoming child relationships. Composite-key relationships use every declared component. Open a related row and repeat to build a chain; Backspace returns one hop at a time.

Inside Related Data, `V` searches exact values across same-named columns in other tables and opens a match as a typed filter. This is an explicit value search, not proof of a foreign-key relationship.

### Sort, page, and size

- `S` toggles sorting on the selected column. Table results use server-side order; ad-hoc query results can only sort the loaded page locally.
- `PgDn` or `]` loads the next page; `PgUp` or `[` loads the previous page; Home/End jump to first/last when the count is known.
- `+` and `-` resize the selected column.
- `Ctrl++` and `Ctrl+-`, or the terminal-safe `>` and `<`, resize all columns.
- `0` or `Ctrl+0` resets the current table's remembered widths.
- `Alt++` and `Alt+-` step through 50, 100, 250, 500, 1,000, and safe-maximum preview sizes. `Alt+0` toggles 100 rows and safe maximum.
- `F5` refreshes the current table. `Ctrl+F5` refreshes schema objects and current data.

The default preview is 100 rows. Rendering is bounded to 1,000 rows, 12,000 cells, and approximately 2 MiB of estimated display data; safe maximum chooses the largest size that stays inside those ceilings. Column widths are persisted per connection/table/column. Table pins persist per database connection. Result row and header positions are remembered per table in the current connection.

## Import SQL and export CSV

`Alt+I` imports a PostgreSQL or MySQL SQL dump into the active connection. Dashboard `I` performs the same operation for the highlighted saved connection. Import streams client output, supports stop-on-error behavior, has a 30-minute operation timeout, and can be canceled with `Esc` or `Ctrl+C`. SQLite, Turso, and D1 do not use this SQL-import screen.

`Alt+E` exports one of three scopes:

- Explicitly selected displayed rows.
- The current displayed page.
- Every table row matching the active filters and sort.

All-matching export streams from the database instead of forcing every row into the TUI. Publication is atomic; cancellation or failure does not leave a completed-looking partial CSV.

## Compare changes with Change Profiler

Press `Alt+W` to open Change Profiler. It creates portable, named before/after anchors without installing triggers or server objects.

1. Press `N`, name the anchor, and review the table plan.
2. Use Space to toggle one table or `A` to explicitly include/exclude the whole database.
3. Capture the baseline, perform the operation you want to observe, then press `S` to scan without stopping or `F` for the final scan and finish.
4. Press `Enter` on a report to inspect changed tables, inserted/updated/deleted rows, schema changes, columns, and complete before/after values.
5. `E` renames and `D` permanently deletes a local anchor/report.

Risky, keyless, or very large tables start excluded and require explicit opt-in. Loaders show phase, table, rows, bytes, rate, elapsed time, approximate percent, and ETA when estimates are meaningful. Baselines are adaptively compressed in private local state.

PostgreSQL/MySQL use a consistent repeatable-read snapshot; SQLite/Turso use a transaction; D1 is reported as best-effort. A scan must use the original connection. Cancellation preserves the last successful report and removes partial capture state.

Change Profiler shows observed differences. The connection and dbterm activity are evidence, but the writer remains **Unknown** unless the database has its own audit trail. Database-native WAL/binlog tracking is not enabled automatically because it needs engine-specific privileges/configuration and can retain server logs when a consumer stops.

## Operate local PostgreSQL and MySQL services

Press `Alt+S` or Dashboard `S` to inspect supported local PostgreSQL/MySQL services. On supported Linux systems the page shows detected status, version, port, PID, and resident memory.

- `1` toggles MySQL and `2` toggles PostgreSQL.
- `C` or `Enter` connects through a saved login or manually entered database credentials.
- Leave Database blank to browse everything visible to that login.
- `R` refreshes service state and `Esc` returns.

Starting/stopping a service may require a system authentication prompt. dbterm distinguishes that OS privilege from the database username/password and gives install guidance when a supported service is absent.

## Back up and restore databases

The same backup engine serves instant TUI backups, durable Backup Center jobs, the CLI, and the background agent. The complete field-by-field operator handbook is available at [the Backup Center documentation](https://dbterm.shreyam1008.com.np/backup.md).

### Required tools and agent PATH

Install only the native tools required by the databases and destinations you use:

- PostgreSQL backup requires `pg_dump`; import, inspection, and restore can require `pg_restore` and `psql`.
- MySQL/MariaDB backup requires `mysqldump`; import and restore require `mysql`.
- SQLite snapshot backup and snapshot restore are built in. Restoring a streamed SQLite SQL dump requires `sqlite3`.
- Turso/LibSQL logical export and Cloudflare D1 native export do not require those database command-line clients.
- An `rclone://...` destination requires `rclone`, configured for the same OS account that runs the interactive app or backup agent.

Install the matching packages:

- Ubuntu: `sudo apt install postgresql-client mysql-client sqlite3 rclone`
- Debian: `sudo apt install postgresql-client default-mysql-client sqlite3 rclone`
- macOS with Homebrew: `brew install libpq mysql-client sqlite rclone`
- Windows: install the PostgreSQL and MySQL command-line clients when those engines are used, install `sqlite3` from the [official SQLite tools downloads](https://sqlite.org/download.html), and install `rclone` from the [official rclone downloads](https://rclone.org/downloads/). Add their executable directories to the PATH of the user or system account that runs the backup agent.

Verify the relevant tools from that same account:

```text
pg_dump --version
pg_restore --version
psql --version
mysqldump --version
mysql --version
sqlite3 --version
rclone version
```

Homebrew's `libpq` and `mysql-client` formulae may be keg-only, so make their `bin` directories available to the agent rather than relying only on an interactive shell profile. For remote storage, run `rclone config` and a read check such as `rclone lsd offsite:` as the agent account before saving `rclone://offsite/...`. A Windows system task cannot use a per-user mapped-drive letter; use a local path, UNC/mounted path available to that account, or rclone.

### Instant backup

Press `Alt+B` from Tables, Query, or Results. Choose an absolute local/mounted folder or `rclone://remote/path`; `F2` opens the native folder chooser and `F3` refreshes destination and private-staging capacity. dbterm chooses the engine-appropriate format, refuses to overwrite an existing file, cancels safely, and publishes only a finished artifact. The default folder is `~/dbterm-backups`.

### Durable plans

Open Backup Center with `Alt+K` or Dashboard `B`. Press `N` to select a saved database or add one; Dashboard `Ctrl+B` starts with its highlighted connection, and `Ctrl+N` adds another connection from inside the plan form.

A plan configures:

- Manual, interval, daily, or weekly scheduling with timezone, weekdays, catch-up behavior, and enabled state.
- Absolute local/mounted or rclone destinations.
- Filename tokens: `{job}`, `{connection}`, `{database}`, `{engine}`, `{date}`, `{time}`, `{timestamp}`, and `{run}`.
- Count, maximum-age, and total-size retention ceilings. The newest verified artifact is always preserved.
- Timeout and compression: none, gzip, ZIP, or Zstandard with a chosen level.
- Optional age X25519 public recipient.
- Email policy: never, failure, success, or both; Gmail STARTTLS defaults or custom SMTP/TLS, recipients, sender, username, and app password.

Backup Center actions include run now, enable/disable schedule, apply retention, history, inspect/restore, desktop/server agent status and controls, age key generation, edit, and delete. Deleting a job keeps its history and files.

### Agents, inspection, and restore

Desktop/user mode requires no administrator access and starts at login. Server/system mode is an explicit elevated installation that starts at machine boot and requires existing config/state/log paths plus a chosen runtime user. Start/stop changes runtime; enable/disable changes startup policy independently. The status screen separates registration, startup, manager runtime, process lock, heartbeat, PID, uptime, memory, and current phase. Logs can be refreshed and copied.

Inspection unwraps at most three gzip, Zstandard, single-entry ZIP, or age layers in private OS temporary files, with a 1 GiB decoded limit per layer by default. It identifies the database format from content, checks SHA-256 history when available, and warns about misleading extensions. Increase **Max Decoded GiB** only for a trusted larger artifact and provide enough temporary disk space.

Restore is preview-first and always requires explicit consent. The detected engine must match the saved target. Merge is the default; clean restore is a separate destructive choice and requires the exact database name or normalized SQLite path. Valid SQL can still perform any server-side action allowed to the selected database account, so restore only trusted files.

## Configure Settings and shortcuts

Open Settings with Dashboard `G`, `Alt+,`, or `Alt+G`. Settings owns:

- Dashboard health checks: `auto` or `manual`.
- Agent connection scope: only the active saved profile (default) or all saved profiles.
- **Allow Agent Profile Writes**, disabled by default because profiles can contain credentials.
- Every configurable global key binding.

Settings validates names, modifier requirements, duplicates, and reserved contextual keys before saving. `Ctrl+S` saves the form; Reset Defaults restores the built-in bindings. `Ctrl+Space`, Tab, Esc, Backspace, `F5`, `Ctrl+C`, and result-sizing keys remain context-owned and cannot be reassigned as unsafe global plain-letter shortcuts.

Default configurable actions:

| Default | Action |
| --- | --- |
| `Alt+T` / `Alt+Q` / `Alt+R` | Focus Tables / Query / Results |
| `Alt+D` | Dashboard |
| `Alt+H` | Guide & SQL Reference |
| `Alt+S` | Database services |
| `Alt+F` | Fullscreen Results |
| `Alt+B` / `Alt+K` | Instant backup / Backup Center |
| `Alt+W` | Change Profiler |
| `Alt+E` | CSV export |
| `Alt+Y` | Query history |
| `Alt+,` / `Alt+G` | Settings |
| `Alt+I` | SQL import |
| `Alt+M` | Schema inspection |
| `Alt+A` / `Alt+C` | Select all displayed rows / clear selection |
| `Ctrl+P` | Command palette |

The **Keyboard & workflows** section of the in-app guide renders effective configured shortcuts, not stale defaults.

## Connect trusted AI agents through MCP

`dbterm mcp serve` starts a local Model Context Protocol server over standard input/output. It opens no network port and does not require a hosted dbterm account. The complete agent guide is available at [dbterm for agents](https://dbterm.shreyam1008.com.np/agents/) and as [agent-readable Markdown](https://dbterm.shreyam1008.com.np/agents.md).

Register it with Codex:

```bash
codex mcp add dbterm -- dbterm mcp serve
```

Or configure any MCP client with command `dbterm` and arguments `mcp`, `serve`.

Available tools are `list_connections`, `inspect_database`, `inspect_table`, `query_read_only`, `explain_query`, `follow_record`, and—only after explicit Settings opt-in—`save_connection_profile`. The server also exposes `dbterm://mcp/instructions`.

Runtime options:

```text
--connection active|all|ID
--max-rows N              default 50, range 1–200
--timeout 8s              maximum 30s
--deny-profile-write
```

Database execution is always read-only, one statement at a time, and bounded by query size, time, rows, columns, cell size, and response bytes. Stored passwords/tokens are never returned. Calls are audited to stderr without SQL text or credentials. Profile writes remain behind the Settings gate and secret inputs are write-only. A SELECT-only database account is still recommended because syntax checks cannot prove that every database-specific function is free of side effects.

## Database SQL reference

These examples are starting points, not a substitute for reviewing the target database and account privileges. Replace placeholder table names and add a narrow `WHERE` clause before running writes.

### PostgreSQL

```sql
-- Schema and server
SELECT table_name FROM information_schema.tables WHERE table_schema = 'public';
SELECT column_name, data_type, is_nullable
  FROM information_schema.columns WHERE table_name = 'TABLE';
SELECT indexname, indexdef FROM pg_indexes WHERE tablename = 'TABLE';
SELECT version(), current_database(), current_user;
SELECT pg_size_pretty(pg_database_size(current_database()));

-- Data and performance
SELECT * FROM TABLE LIMIT 100;
SELECT COUNT(*) FROM TABLE;
EXPLAIN ANALYZE SELECT * FROM TABLE WHERE id = 1;
SELECT pg_size_pretty(pg_total_relation_size('TABLE'));
SELECT * FROM pg_stat_activity;
```

### MySQL and MariaDB

```sql
-- Schema and server
SHOW TABLES;
DESCRIBE TABLE_NAME;
SHOW CREATE TABLE TABLE_NAME;
SHOW INDEX FROM TABLE_NAME;
SELECT VERSION(), DATABASE(), USER();
SHOW DATABASES;

-- Data and performance
SELECT * FROM TABLE_NAME LIMIT 100;
SELECT COUNT(*) FROM TABLE_NAME;
EXPLAIN SELECT * FROM TABLE_NAME WHERE id = 1;
SHOW TABLE STATUS;
SHOW PROCESSLIST;
```

### SQLite

```sql
-- Schema and database
SELECT name FROM sqlite_master WHERE type = 'table';
PRAGMA table_info(TABLE_NAME);
SELECT sql FROM sqlite_master WHERE name = 'TABLE_NAME';
SELECT sqlite_version();
PRAGMA database_list;
PRAGMA integrity_check;

-- Data and performance
SELECT * FROM TABLE_NAME LIMIT 100;
SELECT COUNT(*) FROM TABLE_NAME;
EXPLAIN QUERY PLAN SELECT * FROM TABLE_NAME WHERE id = 1;
PRAGMA optimize;
ANALYZE;
```

### Turso / LibSQL

Turso uses SQLite-compatible catalog and query syntax through LibSQL. Start with `sqlite_master`, `PRAGMA table_info(TABLE_NAME)`, `SELECT sqlite_version()`, bounded `SELECT` queries, and `EXPLAIN QUERY PLAN`. Availability of individual pragmas can depend on the remote service.

### Cloudflare D1

D1 also uses SQLite-compatible SQL through the Cloudflare API. Use `sqlite_master`, `PRAGMA table_info(TABLE_NAME)`, bounded `SELECT` queries, and D1-supported pragmas. Account ID, database ID, and token are connection metadata, not SQL values.

## CLI reference

### Top-level commands

| Command | Purpose |
| --- | --- |
| `dbterm` | Launch the TUI |
| `dbterm --help` | Print command and shortcut help |
| `dbterm --version` | Print version and build information |
| `dbterm --info` | Print executable, config, state, and runtime information |
| `sudo dbterm connections recover-sudo` | Merge unique connections saved by older sudo-launched versions |
| `dbterm mcp serve [options]` | Start the local read-only MCP server |
| `dbterm --update [X.Y.Z]` | Install the latest or requested release |
| `dbterm --uninstall [--yes] [--purge]` | Remove the binary and optionally dbterm-owned data |

Common `help`, `version`, `info`, `update`, and `remove` aliases are accepted, but scripts should use the documented forms.

### Backup CLI

```text
dbterm backup list|jobs [--json]
dbterm backup create --connection <id|name> --destination <folder|rclone://remote/path>
  [--name LABEL] [--filename TEMPLATE]
  [--compression none|gzip|zip|zstd] [--level N]
  [--age-recipient AGE1...] [--timeout MINUTES]
dbterm backup run <job-id|name>
dbterm backup prune --yes <job-id|name>
dbterm backup inspect [--identity key.txt] [--max-decoded-gib N] [--json] <file>
dbterm backup restore --connection <id|name> [--identity key.txt]
  [--mode merge|clean] [--stop-on-error=true|false]
  [--single-transaction=true|false] [--max-decoded-gib N]
  [--timeout DURATION] [--confirm-clean EXACT_TARGET] --yes <file>
dbterm backup status [--json]
dbterm backup service <install|uninstall|start|stop|restart|enable|disable|status>
  [--user|--system|--scope user|system]
  [--run-as USER] [--config-dir PATH] [--state-dir PATH] [--log-dir PATH]
dbterm backup service status --all
dbterm backup keygen [--output identity.txt]
dbterm backup notify-test <job-id|name>
dbterm backup paths [--json]
dbterm backup logs|log [--lines 1..5000] [--previous]
dbterm backup agent [--poll 30s]
dbterm backup run-due
```

`backup create --timeout` is measured in minutes; restore `--timeout` accepts a Go duration such as `45m` and `0` disables that optional timeout. Clean mode requires `--confirm-clean` with the exact target database name or normalized absolute SQLite path. `--run-as` applies only to system service installation, which also requires explicit config, state, and log directories. `service status --all` is the only action that accepts `--all`. `backup agent` and `backup run-due` are headless/internal execution paths for containers and the native scheduler.

Run `dbterm backup --help` for the exact flags accepted by the installed version.

## Files, profiles, and recovery

Use these commands instead of guessing paths:

```bash
dbterm --info
dbterm backup paths
```

Typical roots are:

| OS | Config | State/logs |
| --- | --- | --- |
| Linux | `${XDG_CONFIG_HOME:-~/.config}/dbterm` | `${XDG_STATE_HOME:-~/.local/state}/dbterm` |
| macOS | `~/Library/Application Support/dbterm` | `~/Library/Logs/dbterm` plus application state |
| Windows | `%AppData%\dbterm` | `%LocalAppData%\dbterm` |

dbterm keeps one stable profile for the signed-in OS user. If `sudo dbterm` is launched accidentally, the interactive app hands control back to the invoking user before reading or writing connections, settings, backup plans, or profiler state. Explicit elevated service, update, uninstall, and sudo-recovery operations retain their requested privilege.

Connections have a private primary file, current recovery mirror, previous generation, and state-directory vault. Settings are mirrored to the recovery vault. Missing or unreadable primary data can be restored automatically while a corrupt original is preserved for diagnosis.

Older releases could save connections under root. When detected, recover them non-destructively:

```bash
sudo dbterm connections recover-sudo
```

The command merges only unique profiles, backs up the existing user file, restores user ownership, and leaves the older root file unchanged.

## Troubleshooting

### dbterm is not found after install

Open a new terminal, inspect the installer's reported directory, and confirm it is on `PATH`. Run the binary by its full path once, then use `dbterm --info`.

### Connection fails

Check host, port, database credentials, SSL mode, firewall/VPN reachability, and whether the service is running. Use the form's **Test** action. For a PostgreSQL/MySQL server login, leave the database empty and use **Find DBs** to separate server access from a wrong database name.

### MySQL root works with sudo but not dbterm

The account probably uses local socket authentication. Use a TCP-capable MySQL account with the required database grants; do not enter the Linux sudo password as the MySQL password.

### A query is stuck or the UI is busy

Press `Esc` or `Ctrl+C`. Query, import, export, refresh, backup, and profiler loaders expose safe cancellation. Wait for the final outcome before assuming the database stopped the operation.

### Results are too large

Use filters, a narrower query, or the default bounded preview. For a complete table export, choose **All matching** in `Alt+E`; it streams filtered rows without rendering them all.

### A table or column seems missing

Press `Ctrl+F5` to refresh schema objects. Clear Tables type-ahead with `Esc`, expand the correct schema branch, or search through `Ctrl+P`.

### A scheduled backup did not run

Open Backup Center → `A`, or run:

```bash
dbterm backup service status --all
dbterm backup status
dbterm backup logs --lines 200
```

Confirm registration, startup policy, manager runtime, heartbeat, job enabled state, next run, destination capacity, and that the native clients required by that job are in the agent account's `PATH`. SQLite snapshot jobs need no `sqlite3`; local destinations need no `rclone`.

### Server agent installation fails

Run `dbterm backup paths` as the intended user, create and verify the exact directories, then use the elevated command printed by Backup Center. Server mode never silently falls back to desktop mode. On Windows, a system task cannot rely on a user's mapped drive.

### An encrypted backup is locked

Select the matching age identity during inspection or pass `--identity`. Jobs store the public recipient, not the private key. Without that identity the encrypted contents cannot be restored.

### Restore reports the wrong or ambiguous engine

dbterm trusts file contents, not extensions. Generic SQL that cannot be identified safely is inspectable but blocked from restore. Choose an artifact created by the matching engine and review every warning.

### Update or uninstall reports a permission error

Use the package manager that owns the binary or elevate only the executable replacement/removal. Keep normal app and backup operations in the signed-in user's profile.

### Settings or connections were recovered

Read the startup notice and `dbterm --info`. dbterm preserves the unreadable original when possible and identifies the recovery source. Do not delete recovery generations until the restored profiles have been checked.

## Security, privacy, and deliberate limits

- dbterm has no hosted database proxy, remote MCP endpoint, OAuth service, or autonomous agent service.
- Connection and SMTP secrets remain local but are stored as private plaintext files/catalog fields so the app and unattended agent can use them. Use dedicated, revocable, least-privilege credentials.
- MCP database execution uses its separate read-only policy and transaction boundary. The normal Query workspace does not: its optional **Read-Only Guard** is only a first-token convenience check. `WITH`, `EXPLAIN`, and `PRAGMA` statements can bypass that check and may have side effects, so use database-enforced read-only credentials or grants when writes must be impossible.
- SQL import and restore can perform changes allowed by the target database account. Content detection and command guards do not make untrusted SQL safe.
- Change Profiler reports differences, not an authenticated writer identity.
- Turso/D1 backup artifacts are inspectable, but direct restore targets in this release are PostgreSQL, MySQL/MariaDB, and local SQLite.
- The TUI prioritizes bounded previews and keyboard workflows. Use a desktop workbench when you need visual modeling, broad driver ecosystems, or GUI-heavy administration.

Report vulnerabilities privately through the repository's [security advisory flow](https://github.com/shreyam1008/dbterm/security/advisories/new). Remove credentials, SQL data, paths, tokens, and encryption identities from diagnostics.

## Current release and change history

This manual targets dbterm **v0.10.1 “Parvati”**. It adds a mouse-resizable Tables sidebar and object-only sidebar type-ahead with direct Up/Down match navigation, while keeping collapsed-column discovery in the command palette.

Run `dbterm --version` for the installed build and open Dashboard `U` for its release notes and checksum-verified updater. The repository's [release manifest](https://github.com/shreyam1008/dbterm/blob/main/cmd/dbterm/releases.txt) is the version source used by builds, while [GitHub Releases](https://github.com/shreyam1008/dbterm/releases) provides immutable artifacts, checksums, and historical notes. When an installed version differs from this page, prefer that binary's `--help`, `backup --help`, and offline Guide for exact accepted flags.

## Develop, contribute, and get help

Source, issues, releases, and the MIT license live at [github.com/shreyam1008/dbterm](https://github.com/shreyam1008/dbterm). Read `CONTRIBUTING.md` before opening a pull request.

Run the checkout:

```bash
go run ./cmd/dbterm
```

Build and test:

```bash
make build
make test
```

Verify the website:

```bash
cd site
bun install
bun run verify
```

For architecture and module ownership, read [the project reference](https://github.com/shreyam1008/dbterm/blob/main/docs/project-reference.md). For a bug, include the dbterm version, OS, database type, exact action, expected result, actual result, and a redacted reproduction.
