# dbterm

> **A modern, multi-database terminal client.**  
> Manage PostgreSQL, MySQL, and SQLite databases from a beautiful, keyboard-driven TUI.  
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

| Connection Manager | Query Results |
|:---:|:---:|
| ![Connect DB](assets/connect_db.png) | *Run queries & view results* |

## ⌨️ Key Shortcuts

| Key | Action |
|---|---|
| `Alt + S` | **Service Dashboard** |
| `Alt + D` | **Connection Manager** |
| `Alt + Enter` | **Execute Query** |
| `Alt + Q` / `R` | Focus Query / Results |
| `F5` | Refresh Table |
| `Ctrl + C` | Quit |

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
