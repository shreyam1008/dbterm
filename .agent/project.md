# dbterm — Complete Project Reference

> **Single source of truth** for every feature, shortcut, view, footer, CLI command, and file in the project.
> Update this file whenever any of these change.

---

## Table of Contents

- [Overview](#overview)
- [Supported Databases](#supported-databases)
- [CLI Commands & Flags](#cli-commands--flags)
- [TUI Views / Windows](#tui-views--windows)
- [All Keyboard Shortcuts](#all-keyboard-shortcuts)
- [Footer Help Text (All Views)](#footer-help-text-all-views)
- [Status Bar](#status-bar)
- [Connection Form](#connection-form)
- [Backup Modal](#backup-modal)
- [Row Detail View](#row-detail-view)
- [Alert / Loading Modals](#alert--loading-modals)
- [Config & Storage](#config--storage)
- [Go Drivers](#go-drivers)
- [Icons / Glyphs](#icons--glyphs)
- [Color Theme](#color-theme)
- [Site (Astro)](#site-astro)
- [Build & Release](#build--release)
- [GitHub Actions Workflows](#github-actions-workflows)
- [Install Scripts](#install-scripts)
- [File Map](#file-map)
- [Documentation Sync Checklist](#documentation-sync-checklist)

---

## Overview

**dbterm** is an open-source, keyboard-first TUI (terminal UI) client for SQL databases. Single binary, no external runtime needed. Built in Go with [tview](https://github.com/rivo/tview) and [tcell](https://github.com/gdamore/tcell).

- **Repo**: `github.com/shreyam1008/dbterm`
- **Go Module**: `github.com/shreyam1008/dbterm`
- **License**: MIT
- **Website**: https://shreyam1008.github.io/dbterm/

---

## Supported Databases

| DB Type         | `config.DBType` const | Driver (`DriverName()`)  | Go Package                                    | Features                         |
| --------------- | --------------------- | ------------------------ | --------------------------------------------- | -------------------------------- |
| PostgreSQL      | `postgresql`          | `postgres`               | `github.com/lib/pq`                           | Query + Backup + Service control |
| MySQL           | `mysql`               | `mysql`                  | `github.com/go-sql-driver/mysql`              | Query + Backup + Service control |
| SQLite          | `sqlite`              | `sqlite`                 | `modernc.org/sqlite`                          | Query + local file workflows     |
| Turso (LibSQL)  | `turso`               | `libsql`                 | `github.com/tursodatabase/libsql-client-go`   | Cloud SQLite-compatible query    |
| Cloudflare D1   | `d1`                  | `cfd1`                   | `github.com/peterheb/cfd1`                    | D1 API-backed SQL querying       |

### Connection Config Fields (`config.ConnectionConfig`)

| Field        | Used By                    | JSON key        |
| ------------ | -------------------------- | --------------- |
| `Name`       | All                        | `name`          |
| `Type`       | All                        | `type`          |
| `Host`       | PG, MySQL, Turso           | `host`          |
| `Port`       | PG, MySQL                  | `port`          |
| `User`       | PG, MySQL                  | `user`          |
| `Password`   | PG, MySQL                  | `password`      |
| `Database`   | PG, MySQL                  | `database`      |
| `FilePath`   | SQLite                     | `file_path`     |
| `SSLMode`    | PG                         | `ssl_mode`      |
| `AuthToken`  | Turso, D1                  | `auth_token`    |
| `AccountID`  | D1                         | `account_id`    |
| `DatabaseID` | D1                         | `database_id`   |
| `LastUsed`   | All (auto)                 | `last_used`     |
| `Active`     | All (auto)                 | `active`        |

### Default Ports

- PostgreSQL: `5432`
- MySQL: `3306`

---

## CLI Commands & Flags

Defined in `main.go` → `main()` and helper functions.

| Command                          | Short  | Action                                                  |
| -------------------------------- | ------ | ------------------------------------------------------- |
| `dbterm`                         |        | Launch TUI (prints startup banner first)                |
| `dbterm --help`                  | `-h`   | Print usage, shortcuts, databases, quick start          |
| `dbterm --version`               | `-v`   | Print version, release name, build commit, Go/OS info   |
| `dbterm --info`                  | `-i`   | Print version + paths + resources + drivers + install   |
| `dbterm --update`                | `-u`   | Self-update to latest GitHub release                    |
| `dbterm --update X.Y.Z`         |        | Self-update to a specific version                       |
| `dbterm --uninstall`             |        | Remove binary (interactive confirmation)                |
| `dbterm --uninstall --yes`       | `-y`   | Remove binary without prompt                            |
| `dbterm --uninstall --purge`     | `-p`   | Remove binary **and** `~/.config/dbterm/` (connections) |

#### Startup Banner (printed to terminal before TUI)

```
╔══════════════════════════════════╗
║  dbterm vX.Y.Z                  ║
║  Multi-database terminal client ║
╚══════════════════════════════════╝

⬢ PostgreSQL   ⬡ MySQL   ◆ SQLite
Config: ~/.config/dbterm/connections.json

Starting... Press Alt+H for help inside the app.
```

---

## TUI Views / Windows

### 1. Dashboard (`dashboard.go` → `showDashboard()`)

The landing page shown on startup or when pressing `Alt+D` / `Esc` / `Backspace` from main view.

**Components:**
- **Header**: ASCII art title, DB type icons (⬢ ⬡ ◆), MySQL/PG service status indicators
- **Connection List**: Saved connections with status icon, type tag (PG/MY/SL), name, last used time, live reachability check (● online / ○ offline)
- **Footer**: Context-sensitive action hints (responsive to terminal width)
- **Shortcut keys**: `1-9`, `0` for quick-select first 10 connections

### 2. Main Workspace View (`app.go` → `setupUI()`)

The three-panel SQL workspace shown after connecting to a database.

**Panels:**
- **Tables List** (left or top): Shows all tables with count, `Alt+T` to focus
- **Query Editor** (top-right or middle): SQL input area, `Alt+Q` to focus
- **Results Table** (bottom-right or bottom): Query/table results, `Alt+R` to focus
- **Status Bar** (bottom, 1 row): DB info, table count, row count, limit, sort, actions

**Responsive layout:**
- `width >= 110`: Side-by-side (tables left, query+results right)
- `width < 110`: Stacked (tables top, query+results bottom)

### 3. Help Panel (`help.go` → `showHelp()`)

Full-page scrollable help with keyboard shortcuts and SQL cheatsheets.

**Sections:**
- Keyboard Shortcuts (Navigation, Query & Results, Dashboard, Services, CLI Commands)
- SQL Cheatsheets (PostgreSQL, MySQL, SQLite, Turso, D1) — connected DB cheatsheet shown first

### 4. Connection Form (`connect.go` → `showConnectionForm()`)

Modal form for creating or editing connections. Dynamic fields based on selected DB type.

**Buttons:** `Save & Connect`, `Save Only`, `Test`, `Parse DSN`, `Cancel`

### 5. Services Dashboard (`services.go` → `showServiceDashboard()`)

System service management panel for MySQL and PostgreSQL (Linux systemd).

**Shows per service:** Version, Unit, Port, PID, RAM (human-readable), User, Databases
**Actions:** Toggle start/stop with sudo, Connect modal, Refresh

### 6. Backup Modal (`backup.go` → `showBackupModal()`)

Form for creating timestamped SQL dumps (PostgreSQL via `pg_dump`, MySQL via `mysqldump`).

**Fields:** Output Directory, File Name
**Default filename:** `{db}_{type}_{YYYYMMDD_HHMMSS}.sql`

### 7. Row Detail View (`details.go` → `showRowDetail()`)

Modal showing all column/value pairs for a selected row in a vertical table format.

**Actions:** `c` copies CSV to clipboard (xclip/wl-copy/pbcopy)

### 8. Alert Modal (`alert.go` → `ShowAlert()`)

Generic modal with message + OK button, returns to specified page.

### 9. Loading Modal (`services.go` → `showLoadingModal()`)

Non-interactive "please wait" modal shown during async operations.

---

## All Keyboard Shortcuts

### Global (all views)

| Key                | Action                                   | Source                |
| ------------------ | ---------------------------------------- | --------------------- |
| `Ctrl+C`           | Quit                                     | `app.go:583`          |
| `Alt+H`            | Toggle Help panel                        | `app.go:658`          |
| `Alt+D`            | Go to Dashboard                          | `app.go:670`          |
| `Alt+S`            | Open Services dashboard                  | `app.go:676`          |

### Main Workspace

| Key                | Action                                   | Context               |
| ------------------ | ---------------------------------------- | --------------------- |
| `Alt+T`            | Focus Tables list                        | Any                   |
| `Alt+Q`            | Focus Query editor                       | Any                   |
| `Alt+R`            | Focus Results table                      | Any                   |
| `Tab`              | Cycle focus: Tables → Query → Results    | Any                   |
| `Esc`              | Query→Tables; else→Dashboard             | Any                   |
| `Backspace`        | Back to Dashboard (unless in Query)      | Tables/Results        |
| `Alt+Enter`        | Execute query                            | Query editor          |
| `Shift+Enter`      | Insert newline in query                  | Query editor          |
| `F5`               | Refresh current table (keep sort)        | Main                  |
| `Ctrl+F5`          | Full refresh (table list + results)      | Main                  |
| `F`                | Toggle fullscreen results                | Outside Query editor  |
| `B`                | Open Backup modal                        | Outside Query editor  |
| `S`                | Sort by current column                   | Results table         |
| `Enter` / `Space`  | Open row detail view                     | Results table         |
| `Alt+=` / `Alt++`  | Increase preview row limit               | Main                  |
| `Alt+-` / `Alt+_`  | Decrease preview row limit               | Main                  |
| `Alt+0`            | Toggle preview limit (100 ↔ all)         | Main                  |
| `Ctrl+=` / `Ctrl++`| Zoom table columns wider                 | Main                  |
| `Ctrl+-`           | Zoom table columns narrower              | Main                  |
| `Ctrl+0`           | Reset zoom to default                    | Main                  |
| `+` / `=`          | Widen selected column                    | Results table         |
| `-` / `_`          | Narrow selected column                   | Results table         |

### Dashboard

| Key                | Action                                   |
| ------------------ | ---------------------------------------- |
| `Enter`            | Connect to selected connection           |
| `N`                | New connection form                      |
| `E`                | Edit selected connection                 |
| `D`                | Delete selected connection (confirm)     |
| `R`                | Recheck connection reachability          |
| `H`                | Open Help                                |
| `S`                | Open Services dashboard                  |
| `W` / `B` / `Esc`  | Back to workspace (if connected)        |
| `Q`                | Quit                                     |
| `1-9` / `0`        | Quick-select connections 1-10            |

### Services Dashboard

| Key                | Action                                   |
| ------------------ | ---------------------------------------- |
| `1`                | Toggle MySQL start/stop                  |
| `2`                | Toggle PostgreSQL start/stop             |
| `C` / `Enter`      | Open connect modal                      |
| `R`                | Refresh service info                     |
| `Esc`              | Back                                     |

### Connection Form

| Key                | Action                                   |
| ------------------ | ---------------------------------------- |
| `Tab`              | Navigate between fields                  |
| `Esc`              | Cancel and go back                       |

### Row Detail View

| Key                | Action                                   |
| ------------------ | ---------------------------------------- |
| `Esc` / `Enter`    | Close                                    |
| `C`                | Copy row as CSV to clipboard             |
| `↑` / `↓`          | Scroll through columns                  |

### Help Panel

| Key                | Action                                   |
| ------------------ | ---------------------------------------- |
| `Esc`              | Close help                               |
| `Alt+H`            | Close help (toggle)                      |

---

## Footer Help Text (All Views)

### Dashboard Footer (`dashboard.go` → `dashboardFooterText()`)

Responsive to terminal width. Shows different text for:
- `width < 74`, `< 100`, `< 128`, `>= 128`
- With/without workspace (connected DB)
- With/without saved connections

**Full width (≥128, has connections, has workspace):**
```
Enter Connect 🔌  │  N New  │  E Edit  │  D Delete  │  S Services 🛠  │  R Recheck ↻  │  B/Esc Back ←  │  Q Quit
```

### Main Workspace Status Bar Actions (`app.go` → `statusActionText()`)

Responsive tiers: `< 72`, `< 90`, `< 120`, `>= 120`

**Full width (≥120, in Results):**
```
F5 ↻  │  F Full  │  B 💾  │  Enter Detail  │  Alt+H Help ❓  │  Esc/Bksp Dashboard 🧭
```

**Full width (≥120, in Query editor):**
```
Enter Run ▶  │  Shift+Enter Newline  │  F5 ↻  │  Alt+H Help ❓  │  Esc/Bksp Dashboard 🧭
```

### Connection Form Footer (`connect.go` → `connectFooterText()`)

Per-DB-type text:
- **PostgreSQL/MySQL**: `Tab Navigate  │  Esc Back ←  │  Parse DSN ▼ auto-fills host/user/db`
- **SQLite**: `Tab Navigate  │  Esc Back ←  │  SQLite: only File Path needed`
- **Turso**: `Tab Navigate  │  Esc Back ←  │  Turso: URL + Auth Token`
- **D1**: `Tab Navigate  │  Esc Back ←  │  D1: Account ID + DB ID + Token`

### Services Footer (`services.go` line 57-62)

```
1 Toggle MySQL  │  2 Toggle PostgreSQL  │  R ↻  │  Esc Back ←
```

### Backup Footer (`backup.go` line 106-113)

```
PG user@localhost:5432/database  │  Esc Cancel  │  Backup writes timestamped .sql
```

### Row Detail Footer (`details.go` line 78)

```
Esc/Enter Close  │  c Copy CSV  │  ↑/↓ Scroll
```

---

## Status Bar

Shown at bottom of main workspace (`app.go` → `updateStatusBar()`). Content varies by width:

| Width   | Shows                                                                          |
| ------- | ------------------------------------------------------------------------------ |
| `< 58`  | `○  Alt+H  Q` (offline only)                                                  |
| `< 80`  | `○ offline  │  Alt+H Help  │  Q Quit`                                         |
| `≥ 80`  | DB icon + connection name                                                      |
| `≥ 64`  | + row count                                                                    |
| `≥ 84`  | + preview limit                                                                |
| `≥ 90`  | + table count                                                                  |
| `≥ 104` | + sort status                                                                  |
| `≥ 120` | expanded labels (e.g., `preview 100` instead of `lim:100`)                     |

---

## Connection Form

### Form Fields Per DB Type

| Field                              | PG | MySQL | SQLite | Turso | D1 |
| ---------------------------------- | -- | ----- | ------ | ----- | -- |
| Name (*)                           | ✓  | ✓     | ✓      | ✓     | ✓  |
| Type (*)                           | ✓  | ✓     | ✓      | ✓     | ✓  |
| Connection String (Optional DSN)   | ✓  | ✓     |        |       |    |
| Host                               | ✓  | ✓     |        | ✓*    |    |
| Port                               | ✓  | ✓     |        |       |    |
| User                               | ✓  | ✓     |        |       |    |
| Password                           | ✓  | ✓     |        |       |    |
| Database                           | ✓  | ✓     |        |       |    |
| File Path                          |    |       | ✓      |       |    |
| Auth Token                         |    |       |        | ✓     | ✓  |
| Account ID                         |    |       |        |       | ✓  |
| Database ID (UUID)                 |    |       |        |       | ✓  |

*Turso Host field labeled: "Host (libsql://... or https://...)"

### Validation Rules

- **All**: Name required
- **PG/MySQL**: Host, User, Database required; default port auto-fills
- **SQLite**: FilePath required; parent dir must exist
- **Turso**: Host required
- **D1**: AccountID, DatabaseID, AuthToken all required

---

## Backup Modal

- **Supported**: PostgreSQL (`pg_dump`), MySQL (`mysqldump`)
- **Default dir**: Current working directory
- **Default filename**: `{sanitized_db}_{type}_{YYYYMMDD_HHMMSS}.sql`
- **PG dump args**: `--format=plain --encoding=UTF8`; uses `PGPASSWORD` env, `PGSSLMODE` env
- **MySQL dump args**: `--single-transaction --quick --routines --events --triggers`; uses `MYSQL_PWD` env
- **Timeout**: 20 minutes

---

## Row Detail View

- Opens from Results table via `Enter` or `Space` on any data row
- Shows vertical Column | Value table
- `C` copies as CSV (Column,Value format) using system clipboard (`xclip`, `wl-copy`, or `pbcopy`)

---

## Alert / Loading Modals

- **Alert**: Generic message + OK button, returns to specified page
- **Loading**: Non-interactive spinner with "Please wait..." text

---

## Config & Storage

- **Config directory**: `~/.config/dbterm/`
- **Config file**: `~/.config/dbterm/connections.json`
- **Format**: JSON array of `ConnectionConfig` objects
- **Permissions**: Config file written with `0600`, directory with `0755`

### Store API (`config/config.go`)

| Method              | Description                                    |
| ------------------- | ---------------------------------------------- |
| `LoadStore()`       | Read connections from disk (or empty store)     |
| `store.Save()`      | Write connections to disk                       |
| `store.Add(c)`      | Append + save                                  |
| `store.Update(i,c)` | Replace at index + save                        |
| `store.Delete(i)`   | Remove at index + save                         |
| `store.MarkUsed(i)` | Set Active=true, update LastUsed, deactivate rest |

### Connection Methods

| Method                     | Description                                    |
| -------------------------- | ---------------------------------------------- |
| `cfg.BuildConnString()`    | Generate driver-specific DSN string            |
| `cfg.DriverName()`         | Return Go sql driver name                      |
| `cfg.DisplayLabel()`       | Human-friendly label for display               |
| `cfg.TypeLabel()`          | Pretty DB type name                            |

---

## Go Drivers

All pure Go — no CGO, no C dependencies.

| Database    | Package                                              |
| ----------- | ---------------------------------------------------- |
| PostgreSQL  | `github.com/lib/pq`                                 |
| MySQL       | `github.com/go-sql-driver/mysql`                     |
| SQLite      | `modernc.org/sqlite`                                 |
| Turso       | `github.com/tursodatabase/libsql-client-go/libsql`   |
| D1          | `github.com/peterheb/cfd1`                           |

### Connection Pool Defaults (`utils/db.go`)

```
MaxOpenConns:     3
MaxIdleConns:     1
ConnMaxIdleTime:  2 min
ConnMaxLifetime:  5 min
Ping timeout:     8 sec
```

---

## Icons / Glyphs

Defined in `ui/icons.go`:

| Constant        | Glyph | Usage                      |
| --------------- | ----- | -------------------------- |
| `iconDashboard` | 🧭    | Dashboard title/references |
| `iconConnect`   | 🔌    | Connection-related         |
| `iconHelp`      | ❓    | Help panel                 |
| `iconServices`  | 🛠    | Services dashboard         |
| `iconTables`    | 📚    | Tables panel               |
| `iconQuery`     | 📝    | Query editor               |
| `iconResults`   | 📊    | Results table              |
| `iconBackup`    | 💾    | Backup modal               |
| `iconBack`      | ←     | Navigation back            |
| `iconRefresh`   | ↻     | Refresh actions            |
| `iconDropdown`  | ▼     | Dropdown indicators        |
| `iconInfo`      | ℹ     | Info messages              |
| `iconWarn`      | ⚠     | Warning messages           |
| `iconSuccess`   | ✓     | Success messages           |
| `iconFail`      | ✗     | Failure messages           |

---

## Color Theme

**Catppuccin Mocha** palette, defined in `ui/app.go`:

| Name       | Hex       | Usage                       |
| ---------- | --------- | --------------------------- |
| `bg`       | `#1e1e2e` | Base background             |
| `mantle`   | `#181825` | Form field backgrounds      |
| `crust`    | `#11111b` | Status bar / footers        |
| `green`    | `#a6e3a1` | Success, buttons, active    |
| `surface0` | `#313244` | Selected background         |
| `surface1` | `#45475a` | Borders                     |
| `red`      | `#f38ba8` | Errors, delete, inactive    |
| `peach`    | `#ffb496` | Panel titles, column headers|
| `blue`     | `#89b4fa` | Active border, PG color     |
| `mauve`    | `#cba6f7` | Modal titles, branding      |
| `yellow`   | `#f9e2af` | Shortcut hints, MySQL color |
| `teal`     | `#94e2d5` | Timing, services hints      |
| `text`     | `#cdd6f4` | Primary text                |
| `subtext0` | `#a6adc8` | Secondary text              |
| `overlay0` | `#6c7086` | Placeholders, NULL          |

---

## Site (Astro)

Located in `site/`. Astro static site deployed to GitHub Pages.

### Pages

| File                                 | URL Path         | Content                          |
| ------------------------------------ | ---------------- | -------------------------------- |
| `site/src/pages/index.astro`         | `/`              | Landing page                     |
| `site/src/pages/guide.astro`         | `/guide/`        | Product guide                    |
| `site/src/pages/open-source.astro`   | `/open-source/`  | Open-source handbook             |
| `site/src/pages/robots.txt.ts`       | `/robots.txt`    | SEO robots                       |
| `site/src/pages/sitemap.xml.ts`      | `/sitemap.xml`   | SEO sitemap                      |

### Site Build

```bash
cd site
npm install
npm run dev     # Dev server
npm run build   # Production build
```

### Site Config

- `site/astro.config.mjs` — Astro config
- `site/src/layouts/BaseLayout.astro` — Base layout
- `site/src/styles/global.css` — Global styles
- `site/public/` — Static assets (favicon, manifest, main_ui.png)

---

## Build & Release

### Makefile Targets

| Target          | Command                  | Description                                     |
| --------------- | ------------------------ | ----------------------------------------------- |
| `make build`    | `go build ...`           | Build local binary                              |
| `make clean`    | `rm -f dbterm && rm -rf dist/` | Remove binary and dist                    |
| `make test`     | `go test ./...`          | Run tests                                       |
| `make release`  | `release-core` + `release-ios` | Build all platform binaries              |
| `make release-core` |                      | Build linux/darwin/windows × amd64/arm64        |
| `make release-ios`  |                      | Build iOS arm64 (macOS only, c-archive)         |
| `make deb`      |                          | Build .deb packages                             |
| `make apt-repo` |                          | Build APT repo from .deb                        |

### Build Flags

```
-trimpath
-ldflags="-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)"
CGO_ENABLED=0 (release-core)
```

### Release Artifacts

| File Pattern                        | Platform          |
| ----------------------------------- | ----------------- |
| `dbterm-linux-amd64`                | Linux x64         |
| `dbterm-linux-arm64`                | Linux ARM64       |
| `dbterm-darwin-amd64`               | macOS Intel       |
| `dbterm-darwin-arm64`               | macOS Apple Silicon |
| `dbterm-windows-amd64.exe`          | Windows x64       |
| `dbterm-windows-arm64.exe`          | Windows ARM64     |
| `dbterm_X.Y.Z_amd64.deb`           | Debian x64        |
| `dbterm_X.Y.Z_arm64.deb`           | Debian ARM64      |

### Version Manifest (`releases/versions.txt`)

Format: `<version>|<release name>|<description>` (newest first, SemVer without `v`).
The release workflow reads the first non-comment line.

---

## GitHub Actions Workflows

| File                         | Trigger           | Purpose                                    |
| ---------------------------- | ----------------- | ------------------------------------------ |
| `.github/workflows/ci.yml`      | Push/PR        | Run `go test`                              |
| `.github/workflows/release.yml` | Push to main   | Build binaries, checksums, GitHub Release  |
| `.github/workflows/pages.yml`   | Push to main   | Build & deploy Astro site to GitHub Pages  |

---

## Install Scripts

| Script         | Platform        | Installs to           |
| -------------- | --------------- | --------------------- |
| `install.sh`   | Linux/macOS     | `/usr/local/bin/dbterm` |
| `install.ps1`  | Windows         | `%LOCALAPPDATA%\dbterm\` |

### One-liner Install

```bash
# Linux/macOS
curl -fsSL https://raw.githubusercontent.com/shreyam1008/dbterm/main/install.sh | bash

# Windows PowerShell
powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/shreyam1008/dbterm/main/install.ps1 | iex"
```

---

## File Map

```
dbterm/
├── main.go                  # Entry point: CLI flags, startup banner, update, uninstall
├── go.mod / go.sum          # Go module dependencies
├── Makefile                 # Build, test, release targets
├── README.md                # Project README
├── CONTRIBUTING.md          # Contribution guide
├── install.sh               # Linux/macOS installer
├── install.ps1              # Windows installer
│
├── config/
│   └── config.go            # ConnectionConfig, Store (CRUD), BuildConnString, DriverName
│
├── utils/
│   ├── db.go                # ConnectDB, ListTablesQuery, driver imports
│   └── format.go            # FormatBytes utility
│
├── ui/
│   ├── app.go               # App struct, setupUI, key bindings, responsive layout, Run()
│   ├── dashboard.go         # Dashboard view, connection list, reachability checks
│   ├── connect.go           # Connection form, DSN parsing, test, connectWithConfig
│   ├── tables.go            # LoadTables — fetch table list from DB
│   ├── results.go           # LoadResults — fetch table data, preview limits
│   ├── query.go             # ExecuteQuery — run SQL, detect read/write, error hints
│   ├── populate.go          # populateTable — fill tview.Table from sql.Rows
│   ├── help.go              # Help panel with shortcuts reference + SQL cheatsheets
│   ├── backup.go            # Backup modal, pg_dump/mysqldump execution
│   ├── services.go          # Services dashboard, systemd control, connect modal
│   ├── details.go           # Row detail modal, clipboard CSV copy
│   ├── alert.go             # ShowAlert generic modal
│   └── icons.go             # Shared UI glyphs (emoji icons)
│
├── releases/
│   └── versions.txt         # Release manifest (version|name|description)
│
├── scripts/
│   ├── build-deb.sh         # Debian package builder
│   └── build-apt-repo.sh    # APT repo builder
│
├── assets/
│   ├── main_ui.png          # Screenshot for README
│   └── connect_db.png       # Screenshot
│
├── site/                    # Astro website (GitHub Pages)
│   ├── src/pages/           # index, guide, open-source, robots, sitemap
│   ├── src/layouts/         # BaseLayout.astro
│   ├── src/styles/          # global.css
│   ├── public/              # Static assets
│   ├── astro.config.mjs
│   └── package.json
│
├── dist/                    # Built binaries (release artifacts)
└── .github/workflows/       # CI, Release, Pages workflows
```

---

## Documentation Sync Checklist

When making changes, update **all** of these together:

| What Changed           | Update These Files                                                         |
| ---------------------- | -------------------------------------------------------------------------- |
| Shortcuts              | `ui/help.go`, `ui/app.go` (key bindings), `main.go` (printHelp), `README.md`, `.agent/project.md`, `site/src/pages/guide.astro` |
| Footer help text       | `ui/dashboard.go`, `ui/app.go`, `ui/connect.go`, `ui/services.go`, `ui/details.go`, `.agent/project.md` |
| CLI commands/flags     | `main.go` (printHelp, main switch), `README.md`, `ui/help.go`, `.agent/project.md`, `site/src/pages/guide.astro` |
| New DB type            | `config/config.go`, `utils/db.go`, `ui/connect.go`, `ui/help.go`, `ui/results.go`, `README.md`, `.agent/project.md`, `site/` pages |
| UI view added/changed  | Relevant `ui/*.go`, `ui/help.go`, `.agent/project.md`                     |
| Icons                  | `ui/icons.go`, anywhere icon is referenced                                 |
| Version/release        | `releases/versions.txt` (add as first non-comment line)                    |
| Install experience     | `install.sh`, `install.ps1`, `main.go` (printInfo), `README.md`, site      |
| Site content           | `site/src/pages/`, `site/src/layouts/`, `.agent/project.md`               |
