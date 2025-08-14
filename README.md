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

---

**License**: MIT | **Repo**: [github.com/shreyam1008/dbterm](https://github.com/shreyam1008/dbterm)
