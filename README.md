# dbterm

> A mini DBeaver for your terminal — manage **PostgreSQL**, **MySQL**, and **SQLite** databases from one beautiful TUI.

Built on top of [nabsk911/pgterm](https://github.com/nabsk911/pgterm) — the original PostgreSQL terminal client. This is a rewrite with multi-database support, persistent configurations, and a full connection manager.

---

## ✨ Features

### Multi-Database Engine
- **PostgreSQL** — full support via `lib/pq`
- **MySQL** — full support via `go-sql-driver/mysql`
- **SQLite** — pure Go driver, no CGO needed (`modernc.org/sqlite`)

### Connection Manager (Dashboard)
- Save unlimited database connections
- See all connections at a glance — **type**, **host**, **status** (active/inactive), **last used**
- **Connect**, **Edit**, **Delete** any saved connection
- Dynamic forms — SQLite only asks for file path, PG/MySQL ask for host/port/user/pass/db
- **Save Only** or **Save & Connect** — your choice

### SQL Workspace
- **Table Browser** — click any table to preview its data
- **Query Editor** — write and execute any SQL
- **Result Viewer** — formatted, scrollable results table
- **Status Bar** — shows connected DB type, connection status

### Help & Cheatsheets
- Built-in SQL cheatsheets for each database engine
- Keyboard shortcuts reference
- Common queries, schema inspection commands, performance tips

### Persistent Config
- Connections saved at `~/.config/dbterm/connections.json`
- Survives restarts — open `dbterm` and your connections are waiting

---

## 📦 Installation

```bash
go install github.com/shreyam1008/dbterm@latest
```

Make sure `$GOPATH/bin` (usually `~/go/bin`) is in your `PATH`:

```bash
export PATH=$PATH:$(go env GOPATH)/bin
```

Then run:

```bash
dbterm
```

---

## ⌨️ Keyboard Shortcuts

### Dashboard

| Key     | Action              |
| ------- | ------------------- |
| `Enter` | Connect to selected |
| `N`     | New connection      |
| `E`     | Edit selected       |
| `D`     | Delete selected     |
| `H`     | Help & Cheatsheets  |
| `Q`     | Quit                |

### Main Interface

| Key             | Action             |
| --------------- | ------------------ |
| `Alt + Q`       | Focus Query editor |
| `Alt + R`       | Focus Results view |
| `Alt + T`       | Focus Tables list  |
| `Alt + Enter`   | Execute Query      |
| `Alt + H`       | Toggle Help panel  |
| `Alt + D`       | Back to Dashboard  |
| `Tab/Shift+Tab` | Navigate fields    |
| `Esc`           | Close modal        |

---

## 🗄️ Supported Databases

| Database   | Driver                | Connection Info                    |
| ---------- | --------------------- | ---------------------------------- |
| PostgreSQL | `lib/pq`              | Host, Port, User, Password, DB     |
| MySQL      | `go-sql-driver/mysql` | Host, Port, User, Password, DB     |
| SQLite     | `modernc.org/sqlite`  | File path only (pure Go, no CGO)   |

---

## 🙏 Credits

This project is built on top of **[pgterm](https://github.com/nabsk911/pgterm)** by **[@nabsk911](https://github.com/nabsk911)** — a clean, minimal PostgreSQL TUI client. 

**dbterm** extends it into a multi-database terminal client with connection management, persistent configs, and SQL cheatsheets.

---

## License

MIT
