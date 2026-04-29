package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sjarmak/livedocs/evergreen"
)

// Per-binary pre-migrated SQLite template for the cmd/livedocs evergreen
// tests. Without this, each of the ~17 tests in evergreen_cmd_test.go would
// trigger a fresh CREATE TABLE/INDEX run when the CLI opens the DB. Under
// parallel `go test ./...`, that churn contended with the db and mcpserver
// package binaries on the modernc.org/sqlite connection pool and pushed both
// past the 120s timeout (live_docs-5du).
//
// We copy the template into each test's t.TempDir; the CLI's eventual
// OpenSQLiteStore call still runs Migrate, but every CREATE IF NOT EXISTS is
// a no-op (~0.3ms vs ~50ms for a from-empty migration).

var (
	evergreenTemplateOnce sync.Once
	evergreenTemplatePath string
	evergreenTemplateErr  error
)

func evergreenTemplate(tb testing.TB) string {
	tb.Helper()
	evergreenTemplateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "livedocs-evergreen-template-*")
		if err != nil {
			evergreenTemplateErr = err
			return
		}
		path := filepath.Join(dir, "template.db")
		s, err := evergreen.OpenSQLiteStore(context.Background(), path)
		if err != nil {
			evergreenTemplateErr = err
			return
		}
		if err := s.Close(); err != nil {
			evergreenTemplateErr = err
			return
		}
		evergreenTemplatePath = path
	})
	if evergreenTemplateErr != nil {
		tb.Fatalf("livedocs: build evergreen template: %v", evergreenTemplateErr)
	}
	return evergreenTemplatePath
}

func copyEvergreenTemplate(tb testing.TB, dst string) {
	tb.Helper()
	src, err := os.Open(evergreenTemplate(tb))
	if err != nil {
		tb.Fatalf("livedocs: open template: %v", err)
	}
	defer src.Close()
	out, err := os.Create(dst)
	if err != nil {
		tb.Fatalf("livedocs: create copy: %v", err)
	}
	defer out.Close()
	if _, err := io.Copy(out, src); err != nil {
		tb.Fatalf("livedocs: copy template: %v", err)
	}
}
