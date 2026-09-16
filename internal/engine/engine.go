package engine

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "modernc.org/sqlite"

	"github.com/totoland/sqlmcp/internal/store"
)

type QueryResult struct {
	Columns     []string         `json:"columns"`
	Rows        []map[string]any `json:"rows"`
	RowCount    int              `json:"rowCount"`
	DurationMS  int64            `json:"durationMs"`
	Statement   string           `json:"statement"`
	Mutating    bool             `json:"mutating"`
	RowsAffected int64           `json:"rowsAffected,omitempty"`
}

type TableInfo struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Type   string `json:"type"`
}

type ColumnInfo struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
	PK       bool   `json:"pk"`
}

func Open(c store.Connection) (*sql.DB, error) {
	driver, dsn, err := dsnFor(c)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(5)
	db.SetConnMaxIdleTime(2 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func dsnFor(c store.Connection) (string, string, error) {
	if strings.TrimSpace(c.DSN) != "" {
		switch c.Driver {
		case "postgres":
			return "pgx", c.DSN, nil
		case "mysql":
			return "mysql", c.DSN, nil
		case "mssql":
			return "sqlserver", c.DSN, nil
		case "sqlite":
			return "sqlite", c.DSN, nil
		}
	}
	switch c.Driver {
	case "sqlite":
		path := c.Database
		if path == "" {
			path = c.Host
		}
		if path == "" {
			return "", "", fmt.Errorf("sqlite database path is required")
		}
		return "sqlite", path, nil
	case "postgres":
		host := orDefault(c.Host, "127.0.0.1")
		port := orDefaultInt(c.Port, 5432)
		u := url.URL{
			Scheme: "postgres",
			User:   url.UserPassword(c.User, c.Password),
			Host:   fmt.Sprintf("%s:%d", host, port),
			Path:   "/" + c.Database,
		}
		q := u.Query()
		q.Set("sslmode", "disable")
		u.RawQuery = q.Encode()
		return "pgx", u.String(), nil
	case "mysql":
		host := orDefault(c.Host, "127.0.0.1")
		port := orDefaultInt(c.Port, 3306)
		dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true&charset=utf8mb4&loc=Local",
			c.User, c.Password, host, port, c.Database)
		return "mysql", dsn, nil
	case "mssql":
		host := orDefault(c.Host, "127.0.0.1")
		port := orDefaultInt(c.Port, 1433)
		q := url.Values{}
		q.Set("database", c.Database)
		u := url.URL{
			Scheme:   "sqlserver",
			User:     url.UserPassword(c.User, c.Password),
			Host:     fmt.Sprintf("%s:%d", host, port),
			RawQuery: q.Encode(),
		}
		return "sqlserver", u.String(), nil
	default:
		return "", "", fmt.Errorf("unsupported driver %q", c.Driver)
	}
}

func Execute(ctx context.Context, db *sql.DB, sqlText string, maxRows int, allowWrite bool) (QueryResult, error) {
	sqlText = strings.TrimSpace(sqlText)
	if sqlText == "" {
		return QueryResult{}, fmt.Errorf("SQL is empty")
	}
	if maxRows <= 0 || maxRows > 5000 {
		maxRows = 500
	}
	mutating := IsMutating(sqlText)
	if mutating && !allowWrite {
		return QueryResult{}, fmt.Errorf("write SQL is blocked (read-only). Enable writes on the connection to run INSERT/UPDATE/DELETE/DDL")
	}
	start := time.Now()
	if mutating || !isQuery(sqlText) {
		res, err := db.ExecContext(ctx, sqlText)
		if err != nil {
			return QueryResult{}, err
		}
		affected, _ := res.RowsAffected()
		return QueryResult{
			Columns:      []string{},
			Rows:         []map[string]any{},
			DurationMS:   time.Since(start).Milliseconds(),
			Statement:    firstKeyword(sqlText),
			Mutating:     true,
			RowsAffected: affected,
		}, nil
	}
	rows, err := db.QueryContext(ctx, sqlText)
	if err != nil {
		return QueryResult{}, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return QueryResult{}, err
	}
	out := QueryResult{
		Columns:    cols,
		Rows:       []map[string]any{},
		Statement:  firstKeyword(sqlText),
		Mutating:   false,
	}
	for rows.Next() {
		if len(out.Rows) >= maxRows {
			break
		}
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return QueryResult{}, err
		}
		row := map[string]any{}
		for i, col := range cols {
			row[col] = normalizeValue(raw[i])
		}
		out.Rows = append(out.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return QueryResult{}, err
	}
	out.RowCount = len(out.Rows)
	out.DurationMS = time.Since(start).Milliseconds()
	return out, nil
}

func ListTables(ctx context.Context, db *sql.DB, driver string) ([]TableInfo, error) {
	var q string
	switch driver {
	case "postgres":
		q = `SELECT table_schema, table_name, table_type FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema') ORDER BY 1,2`
	case "mysql":
		q = `SELECT table_schema, table_name, table_type FROM information_schema.tables WHERE table_schema = DATABASE() ORDER BY 1,2`
	case "mssql":
		q = `SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE FROM INFORMATION_SCHEMA.TABLES ORDER BY 1,2`
	default:
		q = `SELECT 'main', name, type FROM sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY name`
	}
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TableInfo{}
	for rows.Next() {
		var t TableInfo
		if err := rows.Scan(&t.Schema, &t.Name, &t.Type); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func DescribeTable(ctx context.Context, db *sql.DB, driver, schema, table string) ([]ColumnInfo, error) {
	var q string
	var args []any
	switch driver {
	case "postgres":
		q = `SELECT c.column_name, c.data_type, c.is_nullable,
		     CASE WHEN k.column_name IS NULL THEN 0 ELSE 1 END
		     FROM information_schema.columns c
		     LEFT JOIN information_schema.key_column_usage k
		       ON k.table_schema=c.table_schema AND k.table_name=c.table_name AND k.column_name=c.column_name
		     WHERE c.table_schema=$1 AND c.table_name=$2
		     ORDER BY c.ordinal_position`
		args = []any{orDefault(schema, "public"), table}
	case "mysql":
		q = `SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_KEY='PRI'
		     FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? ORDER BY ORDINAL_POSITION`
		args = []any{table}
	case "mssql":
		q = `SELECT COLUMN_NAME, DATA_TYPE, IS_NULLABLE,
		     CASE WHEN COLUMNPROPERTY(OBJECT_ID(TABLE_SCHEMA+'.'+TABLE_NAME), COLUMN_NAME, 'IsIdentity')=1 THEN 1 ELSE 0 END
		     FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_SCHEMA=? AND TABLE_NAME=? ORDER BY ORDINAL_POSITION`
		args = []any{orDefault(schema, "dbo"), table}
	default:
		q = `PRAGMA table_info(` + quoteIdent(table) + `)`
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []ColumnInfo{}
		for rows.Next() {
			var cid int
			var name, typ string
			var notnull, pk int
			var dflt any
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				return nil, err
			}
			out = append(out, ColumnInfo{Name: name, Type: typ, Nullable: notnull == 0, PK: pk > 0})
		}
		return out, rows.Err()
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ColumnInfo{}
	for rows.Next() {
		var name, typ string
		var nullable any
		var pk any
		if err := rows.Scan(&name, &typ, &nullable, &pk); err != nil {
			return nil, err
		}
		out = append(out, ColumnInfo{
			Name:     name,
			Type:     typ,
			Nullable: isYes(nullable),
			PK:       isTrue(pk),
		})
	}
	return out, rows.Err()
}

func isYes(v any) bool {
	switch t := v.(type) {
	case string:
		return strings.EqualFold(t, "YES") || t == "1"
	case []byte:
		return isYes(string(t))
	case bool:
		return t
	case int64:
		return t == 1
	default:
		return false
	}
}

func isTrue(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case int64:
		return t != 0
	case int:
		return t != 0
	case []byte:
		return string(t) == "1" || strings.EqualFold(string(t), "true")
	case string:
		return t == "1" || strings.EqualFold(t, "true")
	default:
		return false
	}
}

var mutatingRE = regexp.MustCompile(`(?i)^(WITH\b.+\b)?(INSERT|UPDATE|DELETE|MERGE|REPLACE|CREATE|ALTER|DROP|TRUNCATE|GRANT|REVOKE|VACUUM|ANALYZE|COPY|ATTACH|DETACH|PRAGMA\s+(journal_mode|wal_checkpoint)|CALL|EXEC|EXECUTE)\b`)

func IsMutating(sqlText string) bool {
	kw := firstKeyword(sqlText)
	switch kw {
	case "SELECT", "SHOW", "EXPLAIN", "DESCRIBE", "DESC", "WITH":
		if kw == "WITH" {
			stripped := stripComments(sqlText)
			return mutatingRE.MatchString(strings.TrimSpace(stripped)) && !regexp.MustCompile(`(?i)\bSELECT\b`).MatchString(stripped)
		}
		return false
	case "INSERT", "UPDATE", "DELETE", "MERGE", "REPLACE", "CREATE", "ALTER", "DROP", "TRUNCATE", "GRANT", "REVOKE", "VACUUM", "COPY":
		return true
	default:
		return mutatingRE.MatchString(firstStatement(sqlText))
	}
}

func isQuery(sqlText string) bool {
	kw := firstKeyword(sqlText)
	switch kw {
	case "SELECT", "SHOW", "EXPLAIN", "DESCRIBE", "DESC", "WITH", "PRAGMA", "VALUES":
		return true
	default:
		return false
	}
}

func firstKeyword(sqlText string) string {
	s := strings.TrimSpace(stripComments(sqlText))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) {
			b.WriteRune(unicode.ToUpper(r))
			continue
		}
		break
	}
	return b.String()
}

func firstStatement(sqlText string) string {
	s := strings.TrimSpace(stripComments(sqlText))
	if i := strings.Index(s, ";"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func stripComments(s string) string {
	var out strings.Builder
	inLine, inBlock, inStr := false, false, byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inLine {
			if c == '\n' {
				inLine = false
				out.WriteByte(c)
			}
			continue
		}
		if inBlock {
			if c == '*' && i+1 < len(s) && s[i+1] == '/' {
				inBlock = false
				i++
			}
			continue
		}
		if inStr != 0 {
			out.WriteByte(c)
			if c == inStr {
				inStr = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			inStr = c
			out.WriteByte(c)
			continue
		}
		if c == '-' && i+1 < len(s) && s[i+1] == '-' {
			inLine = true
			i++
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '*' {
			inBlock = true
			i++
			continue
		}
		out.WriteByte(c)
	}
	return out.String()
}

func normalizeValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(t)
	case time.Time:
		return t.Format(time.RFC3339Nano)
	default:
		return t
	}
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func orDefault(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}

func orDefaultInt(v, d int) int {
	if v == 0 {
		return d
	}
	return v
}

func ParseDurationSeconds(v string, fallback int) time.Duration {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		n = fallback
	}
	return time.Duration(n) * time.Second
}
