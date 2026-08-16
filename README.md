# dbterm

Open-source terminal database workbench for local and cloud data.

[![Go Reference](https://pkg.go.dev/badge/github.com/shreyam1008/dbterm.svg)](https://pkg.go.dev/github.com/shreyam1008/dbterm)
[![CI](https://github.com/shreyam1008/dbterm/actions/workflows/ci.yml/badge.svg)](https://github.com/shreyam1008/dbterm/actions/workflows/ci.yml)
[![Latest release](https://img.shields.io/github/v/release/shreyam1008/dbterm)](https://github.com/shreyam1008/dbterm/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-success.svg)](LICENSE)

`dbterm` is one keyboard-first binary for connecting to database servers, exploring and querying data, operating local services, and protecting local or remote databases with durable backups.

Save a PostgreSQL or MySQL server login once—without memorizing a database name—then browse every database that account can access. Open one temporarily with the same credentials, choose a default, or save a separate database connection only when you need one.

## What ships today

| Area | Current capabilities |
| --- | --- |
| **Connections** | PostgreSQL, MySQL/MariaDB, SQLite, Turso/LibSQL, and Cloudflare D1; server-first PostgreSQL/MySQL logins; database discovery; optional defaults; reusable prefilled local/cloud connection forms; one stable per-user profile even after an accidental `sudo dbterm` launch. |
| **Data workspace** | Schema/object discovery, a command/object/recent-SQL palette, persistent table pins, query history, asynchronous cancellable execution, typed results, composable `AND` filters, sorting, first/last pagination, foreign-key navigation, schema inspection, and streamed CSV export. |
| **Database operations** | PostgreSQL/MySQL SQL-dump import with progress and cancellation, plus local MySQL/PostgreSQL service status, start, stop, install guidance, saved-login connection, and server-wide database browsing. |
| **Local agent access** | STDIO MCP server for scoped schema inspection, bounded read-only SQL, query plans, and declared relationship following; stored secrets stay hidden and profile changes require explicit opt-in. |
| **Backup and recovery** | Instant or scheduled backups from local or remote sources to local/mounted or rclone destinations; native dumps, private staging, verification, compression, age encryption, SHA-256 history, retention, email alerts, native OS agents, content inspection, and guarded PostgreSQL/MySQL/SQLite restore. |

The backup routing model covers all four combinations:

| Source | Local / mounted destination | rclone remote destination |
| --- | --- | --- |
| **Local database** | Supported | Supported |
| **Remote / cloud database** | Supported | Supported |

PostgreSQL uses custom `pg_dump` archives; MySQL/MariaDB uses single-database `mysqldump` SQL; SQLite uses a consistent built-in snapshot; Turso uses a single-transaction logical export; and D1 uses Cloudflare's native export API. Restore currently targets PostgreSQL, MySQL/MariaDB, and local SQLite.

See the [complete feature map](https://dbterm.shreyam1008.com.np/features/) or jump to the [Backup Center handbook](docs/backup.md).

Created and maintained by [Shreyam Adhikari (@shreyam1008)](https://shreyam1008.com.np/).

## Quick install

### Linux / macOS

```bash
curl -fsSL https://raw.githubusercontent.com/shreyam1008/dbterm/main/install.sh | bash
```

### Windows (PowerShell)

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/shreyam1008/dbterm/main/install.ps1 | iex"
```

### Go toolchain

```bash
go install github.com/shreyam1008/dbterm/cmd/dbterm@latest
```

### Homebrew

```bash
brew tap shreyam1008/tap
brew install shreyam1008/tap/dbterm
```

### Scoop

```powershell
scoop bucket add shreyam1008 https://github.com/shreyam1008/scoop-bucket
scoop install dbterm
```

## Documentation

- Website: <https://dbterm.shreyam1008.com.np/>
- Complete feature map: <https://dbterm.shreyam1008.com.np/features/>
- Product guide: <https://dbterm.shreyam1008.com.np/guide/>
- AI agent and MCP guide: <https://dbterm.shreyam1008.com.np/agents/>
- Backup Center: <https://dbterm.shreyam1008.com.np/backup/>
- Complete backup handbook: [docs/backup.md](docs/backup.md)
- Marketing, domain, and search plan: [docs/marketing-plan.md](docs/marketing-plan.md)
- Open-source handbook: <https://dbterm.shreyam1008.com.np/open-source/>
- Go package page: <https://pkg.go.dev/github.com/shreyam1008/dbterm>

## Supported databases

| Database | Status |
| --- | --- |
| PostgreSQL | Query + schema inspector + custom-archive backup + content-detected restore + service controls |
| MySQL / MariaDB | Query + schema inspector + single-database SQL backup + content-detected restore + service controls |
| SQLite | Query + schema inspector + consistent snapshot backup + guarded staged snapshot/SQL restore |
| Turso (LibSQL) | Cloud SQLite-compatible querying + schema inspector + transaction-backed logical SQL backup |
| Cloudflare D1 | D1 API-backed SQL querying + schema inspector + Cloudflare-native SQL export |

For MySQL or PostgreSQL, the database name is optional: save the server login once, then choose from every database visible to that account. **Save & Connect** automatically opens the database browser when no default is set. From the Dashboard, highlight a server and press **A** to browse again; **Enter** opens any database with the same saved login, **D** makes it the optional default, and **N** saves it as a separate connection only when that is useful (for example, a scheduled backup). SQLite, Turso, and Cloudflare D1 use the same Dashboard shortcut to prefill their reusable local/cloud scope details.

dbterm keeps one connection profile for the signed-in OS user. Run the TUI normally; if it is accidentally launched as `sudo dbterm`, dbterm immediately hands control back to the invoking user before reading or writing connections, settings, backup plans, or state. Explicit system-service operations, updates, and uninstalls retain the requested elevation. If an older version already saved connections under root, dbterm reports that legacy profile; `sudo dbterm connections recover-sudo` merges its unique connections into the user profile, backs up an existing user file, restores user ownership, and leaves the root file unchanged. In **Services → Connect**, choose a saved local database login or enter a database username/password; the Linux sudo password is not a database password. Ubuntu's MySQL `root` account commonly uses socket-only authentication, so a TCP-capable MySQL user is needed for the interactive client.

## CLI reference

| Command | Purpose |
| --- | --- |
| `dbterm` | Launch TUI |
| `dbterm --help` | Show help |
| `dbterm --version` | Show version/build info |
| `dbterm --info` | Show install/config/runtime info |
| `sudo dbterm connections recover-sudo` | Non-destructively merge connections saved by older sudo-launched versions |
| `dbterm mcp serve` | Start the local read-only MCP server for trusted agents |
| `dbterm --update` | Update to latest release |
| `dbterm --update X.Y.Z` | Update to a specific release |
| `dbterm --uninstall` | Remove binary with confirmation |
| `dbterm --uninstall --yes` | Remove binary without prompt |
| `dbterm --uninstall --purge` | Remove binary + dbterm-owned config, state, and logs; chosen backup artifacts stay |
| `dbterm backup --help` | Backup jobs, agent, inspection, encryption keys, and paths |
| `dbterm backup create …` | Run an instant headless backup from a saved connection |
| `dbterm backup run <job>` | Run a configured job now |
| `dbterm backup prune --yes <job>` | Enforce count/age/size retention immediately |
| `dbterm backup notify-test <job>` | Send a test message with a job's SMTP settings |
| `dbterm backup inspect <file>` | Detect wrappers and database format from file contents |
| `dbterm backup restore --connection <target> --yes <file>` | Inspect, review, and restore into a saved target |
| `dbterm backup service install` | Install and start the desktop/user backup agent |
| `dbterm backup service status --all` | Inspect user and system registrations |
| `dbterm backup service enable / disable` | Change startup policy without changing the current runtime |
| `dbterm backup logs` | Print a bounded tail of the rolling agent log |
| `dbterm backup status` | Show agent heartbeat and configured jobs |

## Core shortcuts

| Shortcut | Action |
| --- | --- |
| `Ctrl + P` (default) | Search commands, database objects, and recent queries in the command palette |
| `Alt + Q / T / R` | Focus Query / Tables / Results |
| `Space` (Tables) | Pin or unpin the highlighted table at the top; saved separately for each database connection |
| Type while Tables is focused | Jump to and highlight the first matching table; Enter opens it and clears the search |
| `Enter` | Execute query (in Query panel) |
| `Shift + Enter` | New line in Query panel |
| `Alt + Y` | Open query history (newest first) |
| `Alt + , / Alt + G` | Open Settings page |
| `Alt + M` | Inspect selected table schema |
| `Alt + A / Alt + C` | Select all result rows / clear selection |
| `Alt + H` | Open help + SQL cheatsheets |
| `G` (Dashboard) | Open Settings page from dashboard |
| `Alt + D` | Return to dashboard |
| `Alt + S` | Open services dashboard |
| `Alt + K` / `B` on Dashboard | Open Backup Center |
| `A` (Dashboard) | Browse all databases visible through the highlighted server login; open one without another saved connection |
| `Ctrl + B` (Dashboard) | Create a backup job with the highlighted saved connection preselected |
| `Alt + B` | Open instant backup from any workspace panel |
| `F2 / F3` (backup forms) | Choose a local destination folder / refresh destination and staging capacity |
| `Alt + F / Alt + I` | Toggle fullscreen results / open import modal (active connection) |
| `I` (Dashboard) | Import SQL dump into selected saved PostgreSQL/MySQL connection |
| `Alt + E` | Export selected rows, current page, or all matching table rows to CSV |
| `C` (Results) | Copy only the selected cell |
| `/` / `V` (Results) | Build typed filters with optional `AND` conditions / apply clipboard equality (`Enter` applies, `Tab` changes controls); remembered per table for the current connection |
| `F` / `Backspace` (Results) | Follow a declared foreign key / return to the previous table |
| `Esc` (filtered Results) | Clear the active filter; press again to return to Dashboard |
| `Alt++ / Alt+- / Alt+0` | Increase / decrease / toggle preview rows per page (`100` ↔ safe max) |
| `+` / `-` (Results) | Widen / narrow the selected column (remembered per table) |
| `Ctrl++ / Ctrl+- / Ctrl+0` (Results) | Resize all columns / reset widths (remembered per table; `Ctrl+=` also widens) |
| `>` / `<` / `0` (Results) | Terminal-safe all-column resize / reset fallback |
| `F5 / Ctrl + F5` | Refresh table / full refresh |
| `Ctrl + C` | Cancel an active query/import/export; otherwise quit |

## Backup Center

For a one-off copy, press `Alt+B` from Tables, Query, or Results; use `F2` for the native folder chooser, type a path, or enter `rclone://remote/path`. `F3` refreshes local destination and private-staging capacity. For durable jobs, press `B` on Dashboard or `Alt+K` anywhere. `N` then chooses an existing saved database or lets you add one; `Ctrl+N` adds another database from the plan form. `Ctrl+B` on a highlighted Dashboard connection starts with it preselected. A job binds one saved local or remote connection to:

- an absolute local/mounted folder or an rclone-backed remote destination, with an optional native GUI folder chooser and local volume/free-space details;
- manual, interval, daily, or weekly timing with an IANA timezone;
- a safe filename template using `{job}`, `{connection}`, `{database}`, `{engine}`, `{date}`, `{time}`, `{timestamp}`, and `{run}`;
- no compression, gzip, ZIP, or single-worker Zstandard with a chosen level;
- optional interoperable age X25519 encryption (the job stores only the public `age1…` recipient);
- latest-count, maximum-age, and maximum-total-size retention;
- optional Gmail-ready or custom SMTP notifications on failure, success, or both; and
- a timeout, enabled state, next run, live byte/phase progress, last result, SHA-256, notification outcome, and artifact history.

Artifacts are staged privately, validated, synced, and published without replacing an existing file. Retention only removes successful artifacts recorded for that job and still contained by its destination; it keeps the newest success and rechecks identity, size, and checksum before deletion. Use `P` in Backup Center or `backup prune --yes` to enforce a changed policy immediately. Removing dbterm, the agent, or a job never removes unrelated or surviving backup files.

“Catch up missed run” executes one overdue occurrence after sleep/restart; it does not replay every missed interval. When catch-up is off, an occurrence more than two minutes late is skipped and the cadence advances to the next future time.

The headless agent has two explicit scopes. Desktop/user mode is the no-admin default:

| OS | Desktop/user registration | Lifetime |
| --- | --- | --- |
| Linux | `systemd --user` | User manager; enable lingering explicitly for post-logout operation |
| macOS | `launchd` LaunchAgent | Logged-in user session |
| Windows | Task Scheduler logon task | Logged-in user session |

Server/system mode is an explicit elevated installation. It starts at boot, preserves the chosen dbterm config/state/log paths, and never silently falls back to desktop mode:

| OS | Server/system registration | Runs as |
| --- | --- | --- |
| Linux | system `systemd` unit | Selected non-root user |
| macOS | `/Library/LaunchDaemons` LaunchDaemon | Selected non-root user |
| Windows | Task Scheduler boot task | LocalSystem; paths must be local and accessible to SYSTEM |

dbterm keeps control data in native per-user locations (run `dbterm backup paths` for the exact paths on the current machine):

| OS | Config | State / catalog | Logs |
| --- | --- | --- | --- |
| Linux | `${XDG_CONFIG_HOME:-~/.config}/dbterm` | `${XDG_STATE_HOME:-~/.local/state}/dbterm` | state directory + `/logs` |
| macOS | `~/Library/Application Support/dbterm` | `~/Library/Application Support/dbterm` | `~/Library/Logs/dbterm` |
| Windows | `%AppData%\dbterm` | `%LocalAppData%\dbterm` | `%LocalAppData%\dbterm\logs` |

Routine scheduler activity is written to the rolling `<logs>/dbterm-backup-agent.log`; its active file and one archive are each capped at 5 MiB. Backup Center can show/copy its tail and `dbterm backup logs` prints it over SSH. Linux/macOS also keep exceptional native-manager stdout/stderr files there. The user registrations are `dbterm-backup.service`, `io.github.shreyam1008.dbterm.backup`, and `dbterm Backup Agent`; system scope uses the machine manager (and a distinct Windows system task).

Saved connections/settings are atomic private JSON files; jobs, leases, run history, checksums, and heartbeats live in `backup/backups.db`. Before wrapping a backup, dbterm keeps the raw native dump and temporary credential file under the private state path `backup/staging`; crash remnants older than 48 hours are removed on a later run. The completed artifact is written only to the destination you selected. Allow enough free space in private state for one uncompressed native dump and enough destination space for one in-progress wrapped artifact.

Unattended jobs reuse the credentials in the saved connection. Those credentials are kept in dbterm's per-user `connections.json` with private-file creation; they are not stored in an OS keyring or encrypted by the backup's age recipient. SMTP app passwords likewise remain plaintext in the private `backups.db` catalog so the agent can authenticate. They are redacted from CLI JSON, UI diagnostics, logs, and SMTP errors. Protect the OS account and dbterm directories, and use dedicated revocable app passwords.

Install it from Backup Center with `A`, or run:

```bash
dbterm backup service install
dbterm backup service status
dbterm backup service status --all
```

Start/stop controls only the current runtime. `backup service enable` and `disable` control boot/login startup independently. Server installation requires explicit existing paths and elevation; on Linux, Backup Center prints a copyable command such as:

```bash
sudo dbterm backup service install --system --run-as "$USER" \
  --config-dir "$HOME/.config/dbterm" \
  --state-dir "$HOME/.local/state/dbterm" \
  --log-dir "$HOME/.local/state/dbterm/logs"
```

Resolve the paths with `dbterm backup paths` before elevation. ACL-only Unix grants may require manual review; Windows server mode cannot rely on a user's mapped drive.

The native manager records the exact dbterm executable path. Service installation therefore rejects disposable `go run` executables: use an installed release or run `make build`, launch `./dbterm`, and install the agent from that stable binary.

The TUI and agent share a transactional SQLite catalog with expiring per-job leases, so manual and scheduled runs cannot overlap. The foreground fallback `dbterm backup agent` is useful in containers or systems without a supported native user manager.

Live progress is deliberately honest: wrapping reports a determinate byte bar and ETA; native clients that do not expose a trustworthy total report live file growth, elapsed time, and activity instead of a fabricated percentage. Agent status separates registration, startup policy, manager runtime, heartbeat, process lock, PID, uptime, resident memory, and any active scheduled phase.

### Encryption

Generate an age identity once:

```bash
dbterm backup keygen
```

Copy the printed public recipient into a job. Keep the private identity separately from off-site backups; it is needed only to inspect or restore encrypted artifacts. dbterm does not store backup passphrases in unattended job configuration.

By default, `keygen` writes the private identity under dbterm's config directory. As a recovery safeguard, `--uninstall --purge` refuses to delete a discovered private age identity or backup-like file. Move the identity somewhere safe—or explicitly remove it yourself—before retrying a purge.

### Inspect and restore

Backup Center → `I` identifies gzip, Zstandard, single-entry ZIP, and age wrappers recursively, then detects PostgreSQL custom/tar/plain SQL, MySQL SQL, SQLite databases, or SQLite SQL from bytes—not the filename. Misleading extensions produce a warning. Locked age files stay “encrypted” until an identity is selected; ambiguous generic SQL is reported but deliberately blocked from restore rather than guessed.

Inspection supports at most three nested wrappers. Each decoded layer defaults to a 1 GiB safety cap and is materialized in the OS temporary directory rather than held in memory. Set **Max Decoded GiB** in the TUI or pass `--max-decoded-gib N` to `inspect` and `restore` for a larger trusted backup; ensure the temporary directory has enough free space for the decoded layers.

The same guarded flow is scriptable. Consent is never implied:

```bash
dbterm backup inspect --identity ./age-identity.txt ./prod.dump.zst.age
dbterm backup restore --connection production --identity ./age-identity.txt --yes ./prod.dump.zst.age
```

Clean mode additionally requires `--confirm-clean` with the exact database name (or normalized SQLite path).

Restore is preview-first. The detected engine must match the chosen saved connection. Merge is the default; PostgreSQL clean restore is opt-in and shown as destructive. PostgreSQL restores use transactional official clients where supported. MySQL warns that earlier statements may remain after a failure. SQLite snapshots and SQL dumps restore through a verified staging database while keeping a pre-restore copy; SQL is streamed through the `sqlite3` client after filesystem-escape checks.

Restore only files from a source you trust. Content detection, checksum revalidation, engine matching, and client-command guards reduce accidental and client-side escape risk; the SQL itself is still allowed to change the selected database and can invoke server-side behavior permitted to that database account.

PostgreSQL/MySQL backup and restore use their official clients; bounded-memory SQLite SQL restore uses `sqlite3` (SQLite snapshot backup/restore remains built in). Install matching tools and keep them in the service PATH:

- Ubuntu/Debian: `sudo apt install postgresql-client mysql-client sqlite3`
- macOS: `brew install libpq mysql-client sqlite`
- Windows: install PostgreSQL/MySQL clients as needed and `sqlite3` from the [official SQLite downloads](https://sqlite.org/download.html)

Remote sources work through saved connections, including reachable cloud databases. Destinations can be absolute local/OS-mounted folders or configured rclone remotes such as `rclone://offsite/dbterm`, so local→local, local→remote, remote→local, and remote→remote backups all use the same verified pipeline. Install rclone, run `rclone config` as the backup-agent OS account, and verify the remote with `rclone lsd offsite:`. Remote credentials stay in rclone rather than dbterm's catalog.

Turso logical exports keep schema and data reads on one source transaction. Virtual/FTS tables are rejected before publication because exporting their shadow tables independently can produce an unrestorable dump. Cloudflare D1 uses Cloudflare's native export API and streams its short-lived signed HTTPS result into dbterm's private staging area; Cloudflare can temporarily make the database unavailable while that export runs. Restore in this release targets PostgreSQL, MySQL/MariaDB, and local SQLite; Turso/D1 SQL backups remain inspectable artifacts.

## Settings + keymap config

- Open settings with `G` from Dashboard or `Alt + ,` / `Alt + G` in workspace.
- Settings use OS-native per-user config directories. Run `dbterm backup paths` to print config, state, logs, catalog, and private-staging locations.
- Key bindings are validated before save (duplicate/invalid mappings are blocked).
- Query history remains enabled per connection; saved-query snippet library is intentionally not included.

## Performance footprint

`dbterm` is tuned for small binary/runtime overhead while staying feature-complete:

- Build strips debug and VCS metadata (`-trimpath -buildvcs=false -ldflags="-s -w -buildid="`).
- The current stripped Linux amd64 build is about 16.0 MiB; `age`, Zstandard, timezone data, and SQLite remain compiled in rather than becoming runtime services.
- The backup catalog opens only when needed; a cold Dashboard is roughly 13 MiB idle RSS in the isolated Linux amd64 smoke test.
- DB pool is intentionally small (`max open=2`, `max idle=1`) for lower idle memory.
- Read-query previews respect the active preview limit (default `100` rows).
- Result rendering is safety-capped by row count, cell count, and estimated display budget.
- `Alt + 0` switches preview to the largest safe page for the current result shape.
- Scheduled work is serialized per agent/job; Zstandard uses one encoder worker and the agent sleeps between catalog checks.

## Repository layout

The project stays intentionally shallow:

| Path | Owns |
| --- | --- |
| `cmd/dbterm/` | Executable entry point, CLI commands, update/uninstall, and release metadata |
| `internal/` | All application-only Go modules, including backup, config, database access, and TUI |
| `docs/` | Feature guides, maintainer reference, and README screenshots |
| `site/` | Astro website, isolated from the Go application |
| `packaging/` | AUR, Homebrew, Scoop, and WinGet definitions |
| `scripts/` | Debian and APT release helpers |

See [docs/project-reference.md](docs/project-reference.md#file-map) for the module-by-module map.

## Build locally

Run the current checkout directly:

```bash
go run ./cmd/dbterm
```

Or build the same optimized local binary used by the release setup, then launch it:

```bash
make build
./dbterm
```

Run tests:

```bash
make test
```

Build website:

```bash
cd site
bun install
bun run verify
```

For a local preview, run `bun run dev` and open the URL Astro prints.

## Release automation

GitHub Actions release workflow reads the first non-comment line in `cmd/dbterm/releases.txt`:

```text
<version>|<release name>|<short description>
```

On push to `main`, it builds artifacts, publishes release assets/checksums, and updates install targets.

## Acknowledgments

dbterm was initially inspired by [pgterm](https://github.com/nabsk911/pgterm) by @nabsk911.

The project is now independently developed and has significantly expanded in scope and features.

## Contributing

Read `CONTRIBUTING.md` for starter-friendly guidance on submitting issues and pull requests.

## License

dbterm is MIT licensed.

- Canonical license file: `LICENSE`
- Open-source + license references: <https://dbterm.shreyam1008.com.np/open-source/>
- Package docs: <https://pkg.go.dev/github.com/shreyam1008/dbterm>
