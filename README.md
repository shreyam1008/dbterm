# dbterm

> **A modern, multi-database terminal client.**  
> Manage PostgreSQL, MySQL, and SQLite databases from super lightweight(~10MB RAM) a beautiful, keyboard-driven TUI.  
> **Standalone single binary.** No Java, no Python, no Node.js required.

![Main UI](assets/main_ui.png)

## 🚀 How it works

`dbterm` is a single binary executable.

1. Download the binary for your OS.
2. Run it.
3. Manage your databases.

## ⚡ Quick Install (No Go Required)

### Linux / macOS

```bash
curl -fsSL https://raw.githubusercontent.com/shreyam1008/dbterm/main/install.sh | bash
```

### Windows (PowerShell)

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/shreyam1008/dbterm/main/install.ps1 | iex"
```

> **Compatible with:**
>
> - 🍎 **macOS** (Intel & Apple Silicon)
> - 🐧 **Linux** (AMD64 & ARM64)
> - 🪟 **Windows** (AMD64 & ARM64 via PowerShell)

---

## 🔁 Automatic Releases

Releases are automated from `.github/workflows/release.yml` on every push to `main`.

The workflow reads the first non-comment line from `releases/versions.txt` in this format:

```text
<version>|<release name>|<short description>
```

Rules:

1. Add a new entry at the top for every release.
2. Use SemVer without `v` (example: `0.3.4`).
3. Push to `main`.

What happens automatically:

1. Build binaries for Linux/macOS/Windows (amd64 + arm64).
2. Create tag `v<version>`.
3. Publish GitHub release assets + checksums.
4. Update the APT repository on `gh-pages`.
5. Stamp binaries with the same manifest version/commit for `dbterm --version`.

If the tag already exists, the workflow fails (prevents duplicate release tags).

Install/update behavior:

- `curl ... install.sh` / `install.ps1` installs from **latest GitHub Release**.
- `go install github.com/shreyam1008/dbterm@latest` installs the **latest tagged release**.
- So both pick up updates after the release workflow publishes a new tag.

---

## ✨ Features

- **Multi-Database**: PostgreSQL, MySQL, SQLite (no CGO required).
- **Service Dashboard**: Manage system services (MySQL/PG) and monitor resources.
- **Keyboard Driven**: Query editor, sortable results, and navigation—all without a mouse.
- **Cross-Platform**: Works on Linux, macOS (Intel & Silicon), and Windows.

## 📸 Screenshots

|          Connection Manager          |        Query Results         |
| :----------------------------------: | :--------------------------: |
| ![Connect DB](assets/connect_db.png) | _Run queries & view results_ |

## ⌨️ Key Shortcuts

| Key                               | Action                                       |
| --------------------------------- | -------------------------------------------- |
| `Alt + H`                         | Toggle Help & SQL cheatsheets                |
| `Alt + Enter`                     | Execute Query                                |
| `F5` / `Ctrl + F5`                | Refresh current table / full refresh         |
| `F` / `B`                         | Fullscreen results / backup modal (PG/MySQL) |
| `Alt + Q` / `Alt + T` / `Alt + R` | Focus Query / Tables / Results               |
| `Alt + =` / `Alt + -` / `Alt + 0` | Row preview limit controls                   |
| `S` / `Enter` / `Space` (Results) | Sort column / open row details               |
| `Alt + D` / `Esc` / `Backspace`   | Back to dashboard                            |
| `Alt + S` (or `S` on Dashboard)   | Service Dashboard                            |
| `Ctrl + C`                        | Quit                                         |

> Press `Alt + H` inside the app for a full cheat sheet.

## 📦 Other Installation Methods

### Manual Download

Download the binary for your system from the [Releases Page](https://github.com/shreyam1008/dbterm/releases).

### Go Install (Optional)

```bash
go install github.com/shreyam1008/dbterm@latest
```

### In-App Update

```bash
dbterm --update
```

Optional specific version:

```bash
dbterm --update 0.3.4
```

### In-App Uninstall

```bash
dbterm --uninstall
dbterm --uninstall --purge
dbterm --uninstall --yes
```

---

**License**: MIT | **Repo**: [github.com/shreyam1008/dbterm](https://github.com/shreyam1008/dbterm)
