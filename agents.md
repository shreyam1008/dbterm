# dbterm MCP server

dbterm includes a local Model Context Protocol server so a trusted agent can inspect and query databases through saved dbterm connection profiles.

## Start it

```sh
dbterm mcp serve
```

The process uses MCP over standard input/output. It does not listen on a network port and dbterm does not advertise a hosted MCP endpoint.

Example client configuration:

```json
{
  "mcpServers": {
    "dbterm": {
      "command": "dbterm",
      "args": ["mcp", "serve"]
    }
  }
}
```

Restart the agent client after changing its MCP configuration. The exact configuration filename and UI vary by client.

## Tools

- `list_connections` returns safe profile metadata. It never returns passwords or tokens.
- `inspect_database` lists schemas and tables.
- `inspect_table` returns columns, primary keys, and declared foreign keys.
- `query_read_only` runs one bounded read-only SQL statement.
- `explain_query` validates a `SELECT` and asks the database for a plan without `ANALYZE`.
- `follow_record` loads one exact record and follows declared incoming and outgoing foreign keys by one hop.
- `save_connection_profile` is available only when profile writes are explicitly enabled. Password and token inputs are write-only.

The server also publishes `dbterm://mcp/instructions` as an MCP resource with the active safety contract.

## Scope and profile writes

Configure Agent Access in dbterm Settings. The safe default exposes only the active saved connection and does not expose profile writes.

Runtime options can narrow or explicitly change that choice:

```sh
dbterm mcp serve --connection active
dbterm mcp serve --connection all
dbterm mcp serve --connection CONNECTION_ID
dbterm mcp serve --deny-profile-write
```

Enabling profile writes in dbterm Settings lets an agent persist credentials into dbterm's existing local connection store. There is no command-line flag that can bypass this settings gate. It does not let the agent retrieve stored secrets. An update fully replaces non-secret profile fields; an empty `password` or `auth_token` preserves that stored secret.

## SQL safety

Database writes are not supported by this MCP server. The query surface rejects multiple statements, mutation and administration keywords, MySQL/MariaDB executable comments, unsafe SQLite pragmas, `SELECT INTO`, and `EXPLAIN ANALYZE`. It limits query size, duration, rows, columns, cell size, and total response bytes. Security-relevant audit records go to stderr and contain an audit ID, tool name, connection ID, status, and duration—not SQL text or credentials.

dbterm requests a read-only database transaction. If a driver cannot enforce one, queries fail unless the saved profile is explicitly marked read-only. A database account with `SELECT`-only grants is still recommended: syntax checks cannot prove that every database-specific or user-defined function has no external side effects.

`follow_record` only uses validated identifiers, parameterized values, and declared foreign keys. It does not guess relationships from similar column names.
