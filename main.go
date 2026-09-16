package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/totoland/sqlmcp/internal/httpserver"
	"github.com/totoland/sqlmcp/internal/mcpserver"
	"github.com/totoland/sqlmcp/internal/store"
)

//go:embed web/*
var webFS embed.FS

func main() {
	addr := flag.String("addr", "127.0.0.1:3847", "HTTP listen address")
	dataDir := flag.String("data", defaultDataDir(), "data directory")
	mcpMode := flag.Bool("mcp", false, "run as MCP stdio server")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		log.Fatal(err)
	}
	st, err := store.Open(filepath.Join(*dataDir, "app.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()

	if *mcpMode || (len(flag.Args()) > 0 && flag.Args()[0] == "mcp") {
		if err := mcpserver.ServeStdio(st); err != nil {
			log.Fatal(err)
		}
		return
	}

	web, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	srv := httpserver.New(st, web)
	fmt.Printf("sqlmcp UI  http://%s\n", *addr)
	fmt.Printf("MCP stdio  sqlmcp --mcp\n")
	log.Fatal(http.ListenAndServe(*addr, srv.Handler()))
}

func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "./data"
	}
	return filepath.Join(home, ".sqlmcp")
}
