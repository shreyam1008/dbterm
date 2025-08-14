# dbterm

Open-source, keyboard-first terminal client for SQL workflows.

`dbterm` is a single binary that lets you connect, query, and operate across multiple databases without heavyweight desktop tooling.

![dbterm main interface](assets/main_ui.png)

## Why dbterm

- Single binary install for Linux, macOS, and Windows.
- Keyboard-driven TUI with fast panel navigation.
- Supports PostgreSQL, MySQL, SQLite, Turso (LibSQL), and Cloudflare D1.
- Built-in service dashboard and backup flow for PostgreSQL/MySQL.
- Low overhead runtime (roughly ~8-12 MB idle in typical use).

## Quick install

### Linux / macOS

```bash
curl -fsSL https://raw.githubusercontent.com/shreyam1008/dbterm/main/install.sh | bash
```

### Windows (PowerShell)

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/shreyam1008/dbterm/main/install.ps1 | iex"
```

## Documentation

- Website: <https://shreyam1008.github.io/dbterm/>
- Product guide: <https://shreyam1008.github.io/dbterm/guide/>
- Open-source handbook: <https://shreyam1008.github.io/dbterm/open-source/>

## Supported databases

| Database | Status |
| --- | --- |
| PostgreSQL | Query + backup + service controls |
| MySQL | Query + backup + service controls |
| SQLite | Query and local file workflows |
| Turso (LibSQL) | Cloud SQLite-compatible querying |
| Cloudflare D1 | D1 API-backed SQL querying |

## CLI reference

| Command | Purpose |
| --- | --- |
| `dbterm` | Launch TUI |
| `dbterm --help` | Show help |
| `dbterm --version` | Show version/build info |
| `dbterm --info` | Show install/config/runtime info |
| `dbterm --update` | Update to latest release |
| `dbterm --update X.Y.Z` | Update to a specific release |
| `dbterm --uninstall` | Remove binary with confirmation |
| `dbterm --uninstall --yes` | Remove binary without prompt |
| `dbterm --uninstall --purge` | Remove binary + saved connections |

## Core shortcuts

| Shortcut | Action |
| --- | --- |
| `Alt + Q / T / R` | Focus Query / Tables / Results |
| `Alt + Enter` | Execute query |
| `Alt + H` | Open help + SQL cheatsheets |
| `Alt + D` | Return to dashboard |
| `Alt + S` | Open services dashboard |
| `F5 / Ctrl + F5` | Refresh table / full refresh |
| `F / B` | Fullscreen results / backup modal |
| `Ctrl + C` | Quit |

## Build locally

```bash
make build
```

Run tests:

```bash
make test
```

Build website:

```bash
cd site
npm install
npm run build
```

## Release automation

GitHub Actions release workflow reads the first non-comment line in `releases/versions.txt`:

```text
<version>|<release name>|<short description>
```

On push to `main`, it builds artifacts, publishes release assets/checksums, and updates install targets.

## Contributing

Read `CONTRIBUTING.md` for starter-friendly guidance on submitting issues and pull requests.

## License

MIT. See `LICENSE`.
