# sqlmcp

Go SQL client in the TablePlus/DataGrip style, with an embedded MCP server.

## Features

- SQL editor + result grid
- Connections: SQLite, PostgreSQL, MySQL, SQL Server
- Schema browser
- Query history
- Read-only by default
- MCP stdio tools for AI agents

## Run UI

```bash
cd ~/git/sqlmcp
go run . -addr 127.0.0.1:3847
```

Open http://127.0.0.1:3847

## MCP

```bash
go run . --mcp
```

Claude / Cursor / Hermes config:

```json
{
  "mcpServers": {
    "sqlmcp": {
      "command": "/Users/totoland/git/sqlmcp/sqlmcp",
      "args": ["--mcp"]
    }
  }
}
```

Hermes YAML:

```yaml
mcp_servers:
  sqlmcp:
    command: "/Users/totoland/git/sqlmcp/sqlmcp"
    args: ["--mcp"]
```

MCP tools:

- `list_connections`
- `list_tables`
- `describe_table`
- `execute_sql` (read-only unless `allow_write=true` and connection is not read-only)
- `query_history`

## Safety

Write SQL is blocked unless:

1. Connection `readOnly` is false
2. UI checkbox **Allow writes** is on, or MCP `allow_write=true`
