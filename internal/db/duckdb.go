package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/marcboeker/go-duckdb"
)

type DuckDB struct {
	db *sql.DB
}

func InitDB() (*DuckDB, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dbDir := filepath.Join(homeDir, ".local", "share", "vmc")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(dbDir, "vmc.db")

	db, err := sql.Open("duckdb", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open duckdb: %w", err)
	}

	// Install and load sqlite_scanner extension
	if _, err := db.Exec("INSTALL sqlite; LOAD sqlite;"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to install/load sqlite extension: %w", err)
	}

	return &DuckDB{db: db}, nil
}

func (d *DuckDB) DB() *sql.DB {
	return d.db
}

func (d *DuckDB) Close() error {
	if d.db != nil {
		return d.db.Close()
	}
	return nil
}

func (d *DuckDB) Ping() error {
	return d.db.Ping()
}
