package evergreen

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Tests in this package each create a fresh SQLite file and run schema
// migrations. Under parallel `go test ./...`, ~20 evergreen + ~17 cmd/livedocs
// fresh opens contend on the modernc.org/sqlite connection pool and stretched
// the db and mcpserver package binaries past 120s (live_docs-5du).
//
// sharedTemplate amortizes the migration cost: build one migrated DB per test
// binary, then copy that file into each test's t.TempDir. Migrate is still
// called by OpenSQLiteStore on the copy, but every CREATE IF NOT EXISTS is a
// no-op, taking ~0.3ms vs ~50ms for a fresh schema build (~143x).

var (
	templateOnce sync.Once
	templatePath string
	templateErr  error
)

// sharedTemplate returns the path to a per-binary, pre-migrated SQLite DB
// suitable for copying into a test's tempdir. The template lives under
// os.MkdirTemp and is intentionally not cleaned up — it is a few KiB and
// the OS handles /tmp lifecycle.
func sharedTemplate(tb testing.TB) string {
	tb.Helper()
	templateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "evergreen-template-*")
		if err != nil {
			templateErr = err
			return
		}
		path := filepath.Join(dir, "template.db")
		s, err := OpenSQLiteStore(context.Background(), path)
		if err != nil {
			templateErr = err
			return
		}
		if err := s.Close(); err != nil {
			templateErr = err
			return
		}
		templatePath = path
	})
	if templateErr != nil {
		tb.Fatalf("evergreen: build shared template: %v", templateErr)
	}
	return templatePath
}

// copyTemplateTo writes a fresh copy of the shared migrated template at dst.
func copyTemplateTo(tb testing.TB, dst string) {
	tb.Helper()
	src, err := os.Open(sharedTemplate(tb))
	if err != nil {
		tb.Fatalf("evergreen: open template: %v", err)
	}
	defer src.Close()
	out, err := os.Create(dst)
	if err != nil {
		tb.Fatalf("evergreen: create copy: %v", err)
	}
	defer out.Close()
	if _, err := io.Copy(out, src); err != nil {
		tb.Fatalf("evergreen: copy template: %v", err)
	}
}
