package httpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"time"

	"github.com/totoland/sqlmcp/internal/engine"
	"github.com/totoland/sqlmcp/internal/store"
)

type Server struct {
	Store   *store.Store
	Web     fs.FS
	Timeout time.Duration
	MaxRows int
}

func New(st *store.Store, web fs.FS) *Server {
	return &Server{
		Store:   st,
		Web:     web,
		Timeout: 30 * time.Second,
		MaxRows: 500,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/connections", s.listConnections)
	mux.HandleFunc("POST /api/connections", s.saveConnection)
	mux.HandleFunc("GET /api/connections/{id}", s.getConnection)
	mux.HandleFunc("DELETE /api/connections/{id}", s.deleteConnection)
	mux.HandleFunc("POST /api/connections/{id}/test", s.testConnection)
	mux.HandleFunc("GET /api/connections/{id}/tables", s.listTables)
	mux.HandleFunc("GET /api/connections/{id}/tables/{table}", s.describeTable)
	mux.HandleFunc("POST /api/connections/{id}/query", s.runQuery)
	mux.HandleFunc("GET /api/history", s.history)
	if s.Web != nil {
		mux.Handle("/", http.FileServer(http.FS(s.Web)))
	}
	return cors(mux)
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true, "app": "sqlmcp"})
}

func (s *Server) listConnections(w http.ResponseWriter, _ *http.Request) {
	items, err := s.Store.ListConnections()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	pub := make([]store.Connection, 0, len(items))
	for _, c := range items {
		pub = append(pub, store.PublicConnection(c))
	}
	writeJSON(w, 200, pub)
}

func (s *Server) getConnection(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.GetConnection(r.PathValue("id"))
	if err != nil {
		writeErr(w, 404, err)
		return
	}
	writeJSON(w, 200, store.PublicConnection(c))
}

func (s *Server) saveConnection(w http.ResponseWriter, r *http.Request) {
	var c store.Connection
	if err := readJSON(r, &c); err != nil {
		writeErr(w, 400, err)
		return
	}
	saved, err := s.Store.UpsertConnection(c)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, store.PublicConnection(saved))
}

func (s *Server) deleteConnection(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.DeleteConnection(r.PathValue("id")); err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) testConnection(w http.ResponseWriter, r *http.Request) {
	c, db, err := s.open(r.PathValue("id"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	_ = c
	_ = db.Close()
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) listTables(w http.ResponseWriter, r *http.Request) {
	c, db, err := s.open(r.PathValue("id"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(r.Context(), s.Timeout)
	defer cancel()
	tables, err := engine.ListTables(ctx, db, c.Driver)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, tables)
}

func (s *Server) describeTable(w http.ResponseWriter, r *http.Request) {
	c, db, err := s.open(r.PathValue("id"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(r.Context(), s.Timeout)
	defer cancel()
	cols, err := engine.DescribeTable(ctx, db, c.Driver, r.URL.Query().Get("schema"), r.PathValue("table"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, cols)
}

type queryReq struct {
	SQL        string `json:"sql"`
	AllowWrite bool   `json:"allowWrite"`
	MaxRows    int    `json:"maxRows"`
}

func (s *Server) runQuery(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, db, err := s.open(id)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	defer db.Close()
	var req queryReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, 400, err)
		return
	}
	allow := req.AllowWrite && !c.ReadOnly
	ctx, cancel := context.WithTimeout(r.Context(), s.Timeout)
	defer cancel()
	start := time.Now()
	res, qerr := engine.Execute(ctx, db, req.SQL, firstPositive(req.MaxRows, s.MaxRows), allow)
	hist := store.HistoryEntry{
		ConnectionID: id,
		SQL:          req.SQL,
		OK:           qerr == nil,
		DurationMS:   time.Since(start).Milliseconds(),
		Source:       "ui",
	}
	if qerr != nil {
		hist.Error = qerr.Error()
	} else if res.Mutating {
		hist.RowCount = int(res.RowsAffected)
	} else {
		hist.RowCount = res.RowCount
	}
	_ = s.Store.AddHistory(hist)
	if qerr != nil {
		writeErr(w, 400, qerr)
		return
	}
	writeJSON(w, 200, res)
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.ListHistory(100)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, items)
}

func (s *Server) open(id string) (store.Connection, *sql.DB, error) {
	c, err := s.Store.GetConnection(id)
	if err != nil {
		return store.Connection{}, nil, err
	}
	db, err := engine.Open(c)
	return c, db, err
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	b, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func firstPositive(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}
