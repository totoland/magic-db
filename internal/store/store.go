package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Connection struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Driver    string    `json:"driver"`
	Host      string    `json:"host,omitempty"`
	Port      int       `json:"port,omitempty"`
	User      string    `json:"user,omitempty"`
	Password  string    `json:"password,omitempty"`
	Database  string    `json:"database,omitempty"`
	DSN       string    `json:"dsn,omitempty"`
	ReadOnly  bool      `json:"readOnly"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type HistoryEntry struct {
	ID           int64     `json:"id"`
	ConnectionID string    `json:"connectionId"`
	SQL          string    `json:"sql"`
	OK           bool      `json:"ok"`
	Error        string    `json:"error,omitempty"`
	RowCount     int       `json:"rowCount"`
	DurationMS   int64     `json:"durationMs"`
	Source       string    `json:"source"`
	CreatedAt    time.Time `json:"createdAt"`
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS connections (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  driver TEXT NOT NULL,
  host TEXT,
  port INTEGER,
  user TEXT,
  password TEXT,
  database_name TEXT,
  dsn TEXT,
  read_only INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS query_history (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  connection_id TEXT NOT NULL,
  sql_text TEXT NOT NULL,
  ok INTEGER NOT NULL,
  error TEXT,
  row_count INTEGER NOT NULL DEFAULT 0,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  source TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_history_created ON query_history(created_at DESC);
`)
	return err
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "cnn_" + hex.EncodeToString(b)
}

func (s *Store) ListConnections() ([]Connection, error) {
	rows, err := s.db.Query(`SELECT id,name,driver,host,port,user,password,database_name,dsn,read_only,created_at,updated_at FROM connections ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		c, err := scanConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetConnection(id string) (Connection, error) {
	row := s.db.QueryRow(`SELECT id,name,driver,host,port,user,password,database_name,dsn,read_only,created_at,updated_at FROM connections WHERE id=?`, id)
	return scanConn(row)
}

func (s *Store) UpsertConnection(c Connection) (Connection, error) {
	now := time.Now().UTC()
	if strings.TrimSpace(c.ID) == "" {
		c.ID = newID()
		c.CreatedAt = now
	} else if existing, err := s.GetConnection(c.ID); err == nil {
		c.CreatedAt = existing.CreatedAt
		if c.Password == "" {
			c.Password = existing.Password
		}
	} else {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	c.Name = strings.TrimSpace(c.Name)
	c.Driver = strings.ToLower(strings.TrimSpace(c.Driver))
	if c.Name == "" {
		return Connection{}, fmt.Errorf("name is required")
	}
	if !validDriver(c.Driver) {
		return Connection{}, fmt.Errorf("unsupported driver %q", c.Driver)
	}
	_, err := s.db.Exec(`
INSERT INTO connections(id,name,driver,host,port,user,password,database_name,dsn,read_only,created_at,updated_at)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  name=excluded.name, driver=excluded.driver, host=excluded.host, port=excluded.port,
  user=excluded.user, password=excluded.password, database_name=excluded.database_name,
  dsn=excluded.dsn, read_only=excluded.read_only, updated_at=excluded.updated_at
`, c.ID, c.Name, c.Driver, c.Host, c.Port, c.User, c.Password, c.Database, c.DSN, boolToInt(c.ReadOnly),
		c.CreatedAt.Format(time.RFC3339Nano), c.UpdatedAt.Format(time.RFC3339Nano))
	return c, err
}

func (s *Store) DeleteConnection(id string) error {
	_, err := s.db.Exec(`DELETE FROM connections WHERE id=?`, id)
	return err
}

func (s *Store) AddHistory(h HistoryEntry) error {
	if h.CreatedAt.IsZero() {
		h.CreatedAt = time.Now().UTC()
	}
	_, err := s.db.Exec(`INSERT INTO query_history(connection_id,sql_text,ok,error,row_count,duration_ms,source,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		h.ConnectionID, h.SQL, boolToInt(h.OK), h.Error, h.RowCount, h.DurationMS, h.Source, h.CreatedAt.Format(time.RFC3339Nano))
	return err
}

func (s *Store) ListHistory(limit int) ([]HistoryEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(`SELECT id,connection_id,sql_text,ok,error,row_count,duration_ms,source,created_at FROM query_history ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var h HistoryEntry
		var created string
		var okInt int
		if err := rows.Scan(&h.ID, &h.ConnectionID, &h.SQL, &okInt, &h.Error, &h.RowCount, &h.DurationMS, &h.Source, &created); err != nil {
			return nil, err
		}
		h.OK = okInt == 1
		h.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, h)
	}
	return out, rows.Err()
}

func PublicConnection(c Connection) Connection {
	c.Password = ""
	return c
}

func validDriver(d string) bool {
	switch d {
	case "postgres", "mysql", "sqlite", "mssql":
		return true
	default:
		return false
	}
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

type scanner interface {
	Scan(dest ...any) error
}

func scanConn(sc scanner) (Connection, error) {
	var c Connection
	var created, updated string
	var port sql.NullInt64
	var host, user, password, database, dsn sql.NullString
	var ro int
	if err := sc.Scan(&c.ID, &c.Name, &c.Driver, &host, &port, &user, &password, &database, &dsn, &ro, &created, &updated); err != nil {
		return Connection{}, err
	}
	c.Host = host.String
	c.User = user.String
	c.Password = password.String
	c.Database = database.String
	c.DSN = dsn.String
	if port.Valid {
		c.Port = int(port.Int64)
	}
	c.ReadOnly = ro == 1
	c.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	c.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return c, nil
}

func MustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
