# pgterm

**pgterm** is a terminal-based interface (TUI) for PostgreSQL, written in Go. It allows you to connect to a PostgreSQL database, run queries, and browse tables directly from your terminal.

## Features

- **Connection Manager**: Easily connect to any PostgreSQL database.
- **Table Browser**: View list of tables in the database.
- **SQL Query Editor**: Write and execute SQL queries.
- **Result Viewer**: View query results in a formatted table.
- **Keyboard Navigation**: Efficient keybindings for quick navigation.

## Shortcuts

| Key Binding         | Action               |
| ------------------- | -------------------- |
| `Alt` + `q`         | Focus Query Input    |
| `Alt` + `r`         | Focus Results View   |
| `Alt` + `t`         | Focus Tables List    |
| `Alt` + `Enter`     | Execute Query        |
| `Tab` / `Shift+Tab` | Navigate Up and Down |

## Installation

Ensure you have Go installed (1.25+ recommended).

```bash
git clone https://github.com/nabsk911/pgterm.git
cd pgterm
go mod tidy
go build -o pgterm
```

## Usage

Run the compiled binary:

```bash
./pgterm
```

## Screenshots

### Connection Form

![Connection Modal](assets/connect_db.png)

### Main Interface

![Main Interface](assets/main_ui.png)
