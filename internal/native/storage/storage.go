// Package storage owns the shared SQLite connection, not feature schemas.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	uri := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	// A Windows drive is a path component, not a URI authority (file:///C:/...).
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(uri.Path, "/") {
		uri.Path = "/" + uri.Path
	}
	q := uri.Query()
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(FULL)")
	uri.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL;
		CREATE TABLE IF NOT EXISTS native_schema (module TEXT PRIMARY KEY, version INTEGER NOT NULL);
		CREATE TABLE IF NOT EXISTS native_settings (name TEXT PRIMARY KEY, enabled INTEGER NOT NULL);`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize native management database: %w", err)
	}
	return db, nil
}

// Migrate applies all missing module migrations atomically and refuses newer schemas.
func Migrate(db *sql.DB, module string, schemas ...string) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var version int
	err = tx.QueryRow("SELECT version FROM native_schema WHERE module=?", module).Scan(&version)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if version > len(schemas) {
		return fmt.Errorf("module %s requires a newer binary (schema %d)", module, version)
	}
	for version < len(schemas) {
		if _, err = tx.Exec(schemas[version]); err != nil {
			return err
		}
		version++
		if _, err = tx.Exec("INSERT INTO native_schema(module,version) VALUES(?,?) ON CONFLICT(module) DO UPDATE SET version=excluded.version", module, version); err != nil {
			return err
		}
	}
	return tx.Commit()
}
