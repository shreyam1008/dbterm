# dbterm project map

This is the maintainer map for the repository. It answers two questions:

1. Where does a change belong?
2. Which file should a new contributor read first?

The layout is intentionally shallow. The Go executable lives in `cmd/dbterm`,
application-only Go modules live directly under `internal`, and the website is
isolated in `site`.

## Repository layout

```text
dbterm/
├── cmd/dbterm/              Go executable and command-line workflows
├── internal/                Application-only Go modules
├── docs/                    Maintainer docs, feature guides, screenshots
├── packaging/               AUR, Homebrew, Scoop, and WinGet definitions
├── scripts/                 Debian and APT release helpers
├── site/                    Astro website
├── .github/workflows/       CI, release, and Pages automation
├── Makefile                 Local build, test, and release commands
├── install.sh               Linux/macOS installer
├── install.ps1              Windows installer
└── README.md                User-facing overview and setup
```

Generated files do not belong in Git:

- `dbterm` is the local binary from `make build`.
- `dist/` contains release artifacts.
- `site/dist/` contains the built website.
- `site/node_modules/` contains website dependencies.

## Go application flow

```text
cmd/dbterm
    │
    ├── internal/ui ─────────────┐
    ├── internal/backup ─────────┤
    └── lifecycle commands       │
                                 ▼
        config · database · history · appdirs
                                 │
                                 ▼
        persist · d1sql · osservice · processinfo
```

`cmd/dbterm` owns process-level concerns. `internal/ui` owns interactive TUI
workflows. `internal/backup` owns headless backup behavior. The smaller internal
modules provide focused infrastructure to both.

## Executable: `cmd/dbterm`

| File | Responsibility |
| --- | --- |
| `main.go` | Parse top-level arguments, print help, or launch the TUI |
| `mcp.go` | Parse and start the local STDIO MCP server |
| `backup_cli.go` | Route and execute backup subcommands |
| `backup_service_args.go` | Parse backup-agent service flags and scope |
| `backup_agent_log.go` | Create and rotate backup-agent logs |
| `backup_logs.go` | Read bounded log tails for the CLI |
| `backup_notify.go` | Send notification test messages |
| `backup_prune.go` | Run retention pruning from the CLI |
| `backup_process_lifecycle.go` | Stop or restart a running backup agent safely |
| `update.go` | Download, validate, and install a new binary |
| `uninstall.go` | Remove the binary and optionally dbterm-owned data |
| `version.go` | Render version, build, path, and install information |
| `releases.txt` | Release manifest embedded by `version.go` |
| `deferred_process_*.go` | Platform-specific child-process behavior |

Tests live beside the implementation they exercise.

## Application modules: `internal`

| Module | Responsibility | Start here |
| --- | --- | --- |
| `appdirs` | Resolve native config, state, cache, and log paths | `appdirs.go` |
| `backup` | Backup jobs, engine, catalog, retention, inspection, restore, agent | `model.go`, then `engine.go` |
| `config` | Saved database connections and user settings | `config.go`, `settings.go` |
| `d1sql` | Cloudflare D1 `database/sql` driver | `driver.go` |
| `database` | Open database connections and build introspection queries | `connection.go`, `queries.go` |
| `folderpicker` | Native folder selection adapters | `picker.go` |
| `format` | Shared human-readable value formatting | `bytes.go` |
| `history` | Persist query history per saved connection | `manager.go` |
| `mcpserver` | Local MCP tools, read-only SQL policy, metadata, and relationship following | `server.go`, then `service.go` |
| `changeprofiler` | Stream compressed named baselines, stable-key hashes, determinate progress, and row/cell/schema diff reports | `capture.go`, `progress.go`, then `diff.go` |
| `osservice` | Manage systemd, launchd, and Windows scheduled tasks | `osservice.go` |
| `persist` | Atomically save JSON files | `jsonfile.go` |
| `processinfo` | Inspect process state and resource usage | `processinfo.go` |
| `ui` | TUI state, screens, keyboard flows, and async loading | `app.go` |

Platform-specific files use Go suffixes such as `_linux.go`, `_darwin.go`, and
`_windows.go`. Shared behavior stays in the unsuffixed file in the same module.

The large-database design and native-log decision boundary are documented in [`change-profiler.md`](change-profiler.md).

## TUI guide: `internal/ui`

The UI is organized by user workflow rather than technical type:

| Files | Workflow |
| --- | --- |
| `app.go`, `keymap.go`, `refresh.go` | Application state, navigation, shortcuts, refresh lifecycle |
| `dashboard.go`, `connect.go`, `database_discovery.go` | Saved connections and database selection |
| `tables.go`, `objects.go`, `metadata.go`, `foreign_keys.go` | Database browsing and schema inspection |
| `query.go`, `query_library.go`, `results.go`, `populate.go` | Query execution and result rendering |
| `result_filter.go`, `result_export.go`, `details.go`, `clipboard.go` | Result interaction and export |
| `backup.go`, `backup_center.go` | Instant backups and durable backup management |
| `import.go` | SQL dump import workflow |
| `services.go` | Local PostgreSQL/MySQL process controls |
| `command_palette.go` | Search and dispatch commands, objects, and history |
| `settings.go`, `help.go`, `alert.go` | Supporting screens and dialogs |

Keep code beside the workflow that owns it. A helper used only by result
filtering belongs in `result_filter.go`; it does not need a new module.

## Backup engine guide: `internal/backup`

| Files | Responsibility |
| --- | --- |
| `model.go`, `schedule.go`, `retention.go` | Domain values and policies |
| `store.go` | SQLite catalog for jobs, runs, leases, and metadata |
| `engine.go`, `engine_helpers.go`, `runner.go` | Execute backup jobs and external tools |
| `staging.go`, `publish_noreplace_*.go`, `sync_directory_*.go` | Private staging and safe publication |
| `inspect.go` | Detect compression, encryption, and backup format |
| `restore*.go` | Plan and execute guarded restores |
| `agent*.go` | Scheduled agent loop, activity, lock, and containment |
| `notification.go` | Email notification policy and delivery |
| `credentials.go`, `key.go`, `client_tools.go` | Secrets, age identities, and tool discovery |

The backup user guide is [backup.md](backup.md).

## Website: `site`

The website is a separate Astro application:

| Path | Responsibility |
| --- | --- |
| `site/src/pages/` | Routes and page content |
| `site/src/components/` | Reusable visual sections |
| `site/src/layouts/` | Shared page shell and metadata |
| `site/src/styles/` | Global design tokens and styles |
| `site/src/scripts/` | Interactive demo behavior |
| `site/public/` | Files copied directly into the built site |

Use Bun for the website commands shown in `site/package.json`.

## Build and release ownership

| Path | Responsibility |
| --- | --- |
| `Makefile` | Canonical local commands: build, test, release, Debian, APT |
| `.github/workflows/ci.yml` | Tests, vet, race tests, and cross-compilation |
| `.github/workflows/release.yml` | Release metadata, binaries, checksums, GitHub release, APT publishing |
| `.github/workflows/pages.yml` | Astro verification and GitHub Pages publishing |
| `cmd/dbterm/releases.txt` | Newest-first version, release name, and release description |
| `packaging/` | Package-manager manifests and maintainer notes |
| `scripts/` | Packaging scripts called by Make and workflows |

## Where should I make a change?

| Change | Primary location | Also check |
| --- | --- | --- |
| CLI flag or top-level command | `cmd/dbterm/main.go` | `README.md`, `internal/ui/help.go`, website guide |
| Backup CLI subcommand | `cmd/dbterm/backup_*.go` | `docs/backup.md`, CLI tests |
| Connection fields or persistence | `internal/config/` | `internal/ui/connect.go`, backup model/tests |
| Database-specific SQL | `internal/database/` | UI caller and query tests |
| TUI shortcut | `internal/ui/keymap.go` | `app.go`, `help.go`, README, website guide |
| TUI screen or interaction | Matching file in `internal/ui/` | Adjacent tests and help text |
| Backup/restore behavior | `internal/backup/` | CLI/UI adapters and `docs/backup.md` |
| OS integration | `internal/osservice/` or `internal/processinfo/` | Every supported platform build |
| Release version | `cmd/dbterm/releases.txt` | Packaging and release notes |
| Website content | `site/src/` | `README.md` when user-facing facts change |

## Verification

From the repository root:

```bash
make test
go vet ./...
make build
```

For website changes:

```bash
cd site
bun ci
bun run verify
```
