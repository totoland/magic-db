package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/totoland/sqlmcp/internal/engine"
	"github.com/totoland/sqlmcp/internal/store"
)

func New(st *store.Store) *server.MCPServer {
	s := server.NewMCPServer(
		"sqlmcp",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithInstructions("SQL client MCP. Default is read-only. Use allowWrite=true only when the connection is not marked read-only."),
	)

	s.AddTool(mcp.NewTool("list_connections",
		mcp.WithDescription("List saved database connections (passwords omitted)."),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		items, err := st.ListConnections()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		pub := make([]store.Connection, 0, len(items))
		for _, c := range items {
			pub = append(pub, store.PublicConnection(c))
		}
		return textJSON(pub)
	})

	s.AddTool(mcp.NewTool("list_tables",
		mcp.WithDescription("List tables/views for a connection."),
		mcp.WithString("connection_id", mcp.Required(), mcp.Description("Saved connection id")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("connection_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		c, db, err := open(st, id)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer db.Close()
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		tables, err := engine.ListTables(ctx, db, c.Driver)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return textJSON(tables)
	})

	s.AddTool(mcp.NewTool("describe_table",
		mcp.WithDescription("Describe columns of a table."),
		mcp.WithString("connection_id", mcp.Required(), mcp.Description("Saved connection id")),
		mcp.WithString("table", mcp.Required(), mcp.Description("Table name")),
		mcp.WithString("schema", mcp.Description("Optional schema")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("connection_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		table, err := req.RequireString("table")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		schema := req.GetString("schema", "")
		c, db, err := open(st, id)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer db.Close()
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		cols, err := engine.DescribeTable(ctx, db, c.Driver, schema, table)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return textJSON(cols)
	})

	s.AddTool(mcp.NewTool("execute_sql",
		mcp.WithDescription("Execute SQL. Read-only by default. Write SQL requires allow_write=true AND a non-readOnly connection."),
		mcp.WithString("connection_id", mcp.Required(), mcp.Description("Saved connection id")),
		mcp.WithString("sql", mcp.Required(), mcp.Description("SQL statement")),
		mcp.WithBoolean("allow_write", mcp.Description("Allow INSERT/UPDATE/DELETE/DDL")),
		mcp.WithNumber("max_rows", mcp.Description("Max result rows, default 200")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("connection_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		sqlText, err := req.RequireString("sql")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		allowWrite := req.GetBool("allow_write", false)
		maxRows := int(req.GetFloat("max_rows", 200))
		c, db, err := open(st, id)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer db.Close()
		allow := allowWrite && !c.ReadOnly
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		start := time.Now()
		res, qerr := engine.Execute(ctx, db, sqlText, maxRows, allow)
		hist := store.HistoryEntry{
			ConnectionID: id,
			SQL:          sqlText,
			OK:           qerr == nil,
			DurationMS:   time.Since(start).Milliseconds(),
			Source:       "mcp",
		}
		if qerr != nil {
			hist.Error = qerr.Error()
			_ = st.AddHistory(hist)
			return mcp.NewToolResultError(qerr.Error()), nil
		}
		if res.Mutating {
			hist.RowCount = int(res.RowsAffected)
		} else {
			hist.RowCount = res.RowCount
		}
		_ = st.AddHistory(hist)
		return textJSON(res)
	})

	s.AddTool(mcp.NewTool("query_history",
		mcp.WithDescription("Recent SQL history from UI and MCP."),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		items, err := st.ListHistory(50)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return textJSON(items)
	})

	return s
}

func ServeStdio(st *store.Store) error {
	return server.ServeStdio(New(st))
}

func open(st *store.Store, id string) (store.Connection, *sql.DB, error) {
	c, err := st.GetConnection(id)
	if err != nil {
		return store.Connection{}, nil, fmt.Errorf("connection not found: %w", err)
	}
	db, err := engine.Open(c)
	return c, db, err
}

func textJSON(v any) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}
