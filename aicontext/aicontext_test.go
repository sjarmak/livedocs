package aicontext

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscover(t *testing.T) {
	// Create a temp directory with AI context files.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "CLAUDE.md"), "# Project\n")
	writeFile(t, filepath.Join(root, "AGENTS.md"), "# Agents\n")
	writeFile(t, filepath.Join(root, ".cursorrules"), "rules\n")

	// Create a nested CLAUDE.md.
	subdir := filepath.Join(root, "pkg", "sub")
	must(t, os.MkdirAll(subdir, 0o755))
	writeFile(t, filepath.Join(subdir, "CLAUDE.md"), "# Sub\n")

	// Create a .github directory with copilot instructions.
	ghDir := filepath.Join(root, ".github")
	must(t, os.MkdirAll(ghDir, 0o755))
	writeFile(t, filepath.Join(ghDir, "copilot-instructions.md"), "# Copilot\n")

	files, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if len(files) < 4 {
		t.Errorf("expected at least 4 files, got %d: %v", len(files), files)
	}

	// Verify known files are found.
	found := make(map[string]bool)
	for _, f := range files {
		found[filepath.Base(f)] = true
	}
	for _, want := range []string{"CLAUDE.md", "AGENTS.md", ".cursorrules", "copilot-instructions.md"} {
		if !found[want] {
			t.Errorf("expected to find %s, not found in %v", want, files)
		}
	}
}

func TestDiscoverEmpty(t *testing.T) {
	root := t.TempDir()
	files, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}

func TestExtractClaimsFromContent_FilePaths(t *testing.T) {
	content := `# Project

The main entry point is ` + "`cmd/livedocs/main.go`" + `.
Configuration lives in ` + "`config/settings.yaml`" + `.
See ` + "`drift/drift.go`" + ` for drift detection logic.
The ` + "`nonexistent/path/file.go`" + ` does not exist.
`

	claims := ExtractClaimsFromContent(content, "CLAUDE.md")

	var filePaths []string
	for _, c := range claims {
		if c.Kind == FilePathClaim {
			filePaths = append(filePaths, c.Value)
		}
	}

	expected := map[string]bool{
		"cmd/livedocs/main.go":     true,
		"config/settings.yaml":     true,
		"drift/drift.go":           true,
		"nonexistent/path/file.go": true,
	}

	for _, fp := range filePaths {
		if !expected[fp] {
			t.Errorf("unexpected file path claim: %q", fp)
		}
		delete(expected, fp)
	}
	for fp := range expected {
		t.Errorf("expected file path claim not found: %q", fp)
	}
}

func TestExtractClaimsFromContent_SkipsCodeBlocks(t *testing.T) {
	content := "# Example\n\n```go\nfunc main() {\n    path := \"internal/fake/path.go\"\n}\n```\n\nSee `real/path.go` for details.\n"

	claims := ExtractClaimsFromContent(content, "CLAUDE.md")

	var filePaths []string
	for _, c := range claims {
		if c.Kind == FilePathClaim {
			filePaths = append(filePaths, c.Value)
		}
	}

	if len(filePaths) != 1 {
		t.Fatalf("expected 1 file path claim, got %d: %v", len(filePaths), filePaths)
	}
	if filePaths[0] != "real/path.go" {
		t.Errorf("expected real/path.go, got %q", filePaths[0])
	}
}

func TestExtractClaimsFromContent_GoImports(t *testing.T) {
	content := "Uses `github.com/live-docs/live_docs/drift` for detection.\nAlso `github.com/spf13/cobra` for CLI.\n"

	claims := ExtractClaimsFromContent(content, "CLAUDE.md")

	var imports []string
	for _, c := range claims {
		if c.Kind == PackageRefClaim {
			imports = append(imports, c.Value)
		}
	}

	expected := map[string]bool{
		"github.com/live-docs/live_docs/drift": true,
		"github.com/spf13/cobra":               true,
	}

	for _, imp := range imports {
		if !expected[imp] {
			t.Errorf("unexpected import claim: %q", imp)
		}
		delete(expected, imp)
	}
	for imp := range expected {
		t.Errorf("expected import claim not found: %q", imp)
	}
}

func TestExtractClaimsFromContent_Dedup(t *testing.T) {
	content := "See `cmd/main.go` for entry point.\nAlso check `cmd/main.go` again.\n"

	claims := ExtractClaimsFromContent(content, "CLAUDE.md")

	count := 0
	for _, c := range claims {
		if c.Kind == FilePathClaim && c.Value == "cmd/main.go" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 deduped claim for cmd/main.go, got %d", count)
	}
}

func TestVerify_ValidPaths(t *testing.T) {
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, "cmd", "app"), 0o755))
	writeFile(t, filepath.Join(root, "cmd", "app", "main.go"), "package main\n")

	claims := []Claim{
		{Kind: FilePathClaim, Value: "cmd/app/main.go", SourceFile: filepath.Join(root, "CLAUDE.md"), Line: 5},
		{Kind: FilePathClaim, Value: "cmd/app/", SourceFile: filepath.Join(root, "CLAUDE.md"), Line: 6},
	}

	findings := Verify(root, claims)

	for _, f := range findings {
		if f.Status != Valid {
			t.Errorf("expected valid for %q, got %s: %s", f.Claim.Value, f.Status, f.Detail)
		}
	}
}

func TestVerify_StalePaths(t *testing.T) {
	root := t.TempDir()

	claims := []Claim{
		{Kind: FilePathClaim, Value: "does/not/exist.go", SourceFile: filepath.Join(root, "CLAUDE.md"), Line: 3},
		{Kind: FilePathClaim, Value: "also/missing/dir/", SourceFile: filepath.Join(root, "CLAUDE.md"), Line: 7},
	}

	findings := Verify(root, claims)

	for _, f := range findings {
		if f.Status != Stale {
			t.Errorf("expected stale for %q, got %s: %s", f.Claim.Value, f.Status, f.Detail)
		}
	}
}

func TestVerify_PackageRef_Local(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module github.com/test/project\n\ngo 1.21\n")
	must(t, os.MkdirAll(filepath.Join(root, "drift"), 0o755))
	writeFile(t, filepath.Join(root, "drift", "drift.go"), "package drift\n")

	claims := []Claim{
		{Kind: PackageRefClaim, Value: "github.com/test/project/drift", SourceFile: filepath.Join(root, "CLAUDE.md"), Line: 1},
		{Kind: PackageRefClaim, Value: "github.com/test/project/nonexistent", SourceFile: filepath.Join(root, "CLAUDE.md"), Line: 2},
	}

	findings := Verify(root, claims)

	if findings[0].Status != Valid {
		t.Errorf("expected valid for local package drift, got %s: %s", findings[0].Status, findings[0].Detail)
	}
	if findings[1].Status != Stale {
		t.Errorf("expected stale for nonexistent package, got %s: %s", findings[1].Status, findings[1].Detail)
	}
}

func TestCheck_Integration(t *testing.T) {
	root := t.TempDir()

	// Set up a mini project structure.
	must(t, os.MkdirAll(filepath.Join(root, "cmd", "app"), 0o755))
	must(t, os.MkdirAll(filepath.Join(root, "drift"), 0o755))
	writeFile(t, filepath.Join(root, "cmd", "app", "main.go"), "package main\n")
	writeFile(t, filepath.Join(root, "drift", "drift.go"), "package drift\n")
	writeFile(t, filepath.Join(root, "go.mod"), "module github.com/test/project\n\ngo 1.21\n")

	// Create a CLAUDE.md with both valid and stale references.
	claudeContent := `# Project

## Structure
- Entry point: ` + "`cmd/app/main.go`" + `
- Drift detection: ` + "`drift/drift.go`" + `
- Missing file: ` + "`old/removed/handler.go`" + `
- Uses ` + "`github.com/test/project/drift`" + ` for detection
- Also ` + "`github.com/test/project/deleted_pkg`" + `
`
	writeFile(t, filepath.Join(root, "CLAUDE.md"), claudeContent)

	report, err := Check(root)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	if len(report.Files) != 1 {
		t.Errorf("expected 1 AI context file, got %d", len(report.Files))
	}
	if !report.HasDrift() {
		t.Error("expected drift to be detected")
	}
	if report.StaleCount < 2 {
		t.Errorf("expected at least 2 stale claims, got %d", report.StaleCount)
	}
	if report.ValidCount < 2 {
		t.Errorf("expected at least 2 valid claims, got %d", report.ValidCount)
	}

	// Verify stale references are the expected ones.
	staleValues := make(map[string]bool)
	for _, f := range report.StaleFindings() {
		staleValues[f.Claim.Value] = true
	}
	if !staleValues["old/removed/handler.go"] {
		t.Error("expected old/removed/handler.go to be stale")
	}
	if !staleValues["github.com/test/project/deleted_pkg"] {
		t.Error("expected github.com/test/project/deleted_pkg to be stale")
	}
}

func TestCheck_NoAIContextFiles(t *testing.T) {
	root := t.TempDir()

	report, err := Check(root)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	if len(report.Files) != 0 {
		t.Errorf("expected 0 files, got %d", len(report.Files))
	}
	if report.HasDrift() {
		t.Error("expected no drift for empty project")
	}
}

func TestFormatReport(t *testing.T) {
	report := &Report{
		Root:        "/tmp/test",
		Files:       []string{"/tmp/test/CLAUDE.md"},
		TotalClaims: 3,
		ValidCount:  2,
		StaleCount:  1,
		Findings: []Finding{
			{Claim: Claim{Kind: FilePathClaim, Value: "cmd/main.go", SourceFile: "/tmp/test/CLAUDE.md", Line: 5}, Status: Valid},
			{Claim: Claim{Kind: FilePathClaim, Value: "old/gone.go", SourceFile: "/tmp/test/CLAUDE.md", Line: 8}, Status: Stale, Detail: "path not found"},
			{Claim: Claim{Kind: PackageRefClaim, Value: "github.com/test/pkg", SourceFile: "/tmp/test/CLAUDE.md", Line: 10}, Status: Valid},
		},
	}

	output := FormatReport(report)

	if output == "" {
		t.Fatal("expected non-empty output")
	}
	// Check that stale reference appears in output.
	if !containsStr(output, "old/gone.go") {
		t.Error("expected stale reference old/gone.go in output")
	}
	if !containsStr(output, "Stale References") {
		t.Error("expected 'Stale References' header in output")
	}
}

func TestIsAIContextFile(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"CLAUDE.md", true},
		{"claude.md", true},
		{"AGENTS.md", true},
		{".cursorrules", true},
		{".windsurfrules", true},
		{"copilot-instructions.md", true},
		{"README.md", false},
		{"CHANGELOG.md", false},
		{"random.txt", false},
	}

	for _, tt := range tests {
		got := isAIContextFile(tt.name)
		if got != tt.want {
			t.Errorf("isAIContextFile(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestFormatReport_NoFiles(t *testing.T) {
	report := &Report{Root: "/tmp/test"}
	output := FormatReport(report)
	if !containsStr(output, "No AI context files found") {
		t.Error("expected 'No AI context files found' in output")
	}
}

func TestFormatReport_AllValid(t *testing.T) {
	report := &Report{
		Root:        "/tmp/test",
		Files:       []string{"/tmp/test/CLAUDE.md"},
		TotalClaims: 2,
		ValidCount:  2,
		StaleCount:  0,
		Findings: []Finding{
			{Claim: Claim{Kind: FilePathClaim, Value: "cmd/main.go", SourceFile: "/tmp/test/CLAUDE.md", Line: 5}, Status: Valid},
		},
	}
	output := FormatReport(report)
	if !containsStr(output, "All references are valid") {
		t.Error("expected 'All references are valid' in output")
	}
}

func TestExtractModulePath(t *testing.T) {
	tests := []struct {
		content string
		want    string
	}{
		{"module github.com/test/project\n\ngo 1.21\n", "github.com/test/project"},
		{"// comment\nmodule example.com/foo\n", "example.com/foo"},
		{"no module here\n", ""},
	}
	for _, tt := range tests {
		got := extractModulePath(tt.content)
		if got != tt.want {
			t.Errorf("extractModulePath(%q) = %q, want %q", tt.content, got, tt.want)
		}
	}
}

func TestVerify_PackageRef_NoGoMod(t *testing.T) {
	root := t.TempDir()
	claims := []Claim{
		{Kind: PackageRefClaim, Value: "github.com/some/pkg", SourceFile: filepath.Join(root, "CLAUDE.md"), Line: 1},
	}
	findings := Verify(root, claims)
	if len(findings) != 1 || findings[0].Status != Valid {
		t.Error("expected valid (skip) when no go.mod present")
	}
}

func TestVerify_PathRelativeToContextFile(t *testing.T) {
	root := t.TempDir()
	subdir := filepath.Join(root, "docs")
	must(t, os.MkdirAll(subdir, 0o755))
	must(t, os.MkdirAll(filepath.Join(subdir, "api", "v1"), 0o755))
	writeFile(t, filepath.Join(subdir, "api", "v1", "schema.yaml"), "openapi: 3.0\n")

	claims := []Claim{
		{Kind: FilePathClaim, Value: "api/v1/schema.yaml", SourceFile: filepath.Join(subdir, "CLAUDE.md"), Line: 2},
	}
	findings := Verify(root, claims)
	if len(findings) != 1 || findings[0].Status != Valid {
		t.Errorf("expected valid for path relative to context file, got %s: %s", findings[0].Status, findings[0].Detail)
	}
}

func TestIsVerifiablePath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		// Valid paths.
		{"cmd/main.go", true},
		{"drift/drift.go", true},
		{"src/components/App.tsx", true},
		{"packages/next/dist/", true},

		// Existing filters.
		{"https://example.com/path", false},
		{"$HOME/dir", false},
		{"*.go", false},
		{"ab", false},
		{"no_slash", false},

		// Glob/wildcard patterns (fix: skip * anywhere in path).
		{"packages/*", false},
		{"src/gradle*/", false},
		{"packages/**/*.ts", false},
		{"react-server-dom-webpack/*", false},

		// Ellipsis patterns (fix: skip ... in path).
		{"tests/.../fakes/", false},
		{"src/test/kotlin/.../gradle/", false},

		// Branch name examples (fix: skip common branch prefixes).
		{"feature/agent-memory", false},
		{"fix/tool-registry-bug", false},
		{"bugfix/login-error", false},
		{"hotfix/security-patch", false},
		{"release/v2.0", false},

		// Prose/code concepts (fix: skip short slash-separated code tokens).
		{"if/else", false},
		{"dx/dy", false},
		{"true/false", false},
		{"input/output", false},
	}
	for _, tt := range tests {
		got := isVerifiablePath(tt.path)
		if got != tt.want {
			t.Errorf("isVerifiablePath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestExtractClaimsFromContent_MarkdownLinks(t *testing.T) {
	content := "See [util/buildProject.kt](https://github.com/JetBrains/kotlin/blob/master/util/buildProject.kt) for details.\n" +
		"Also [config guide](docs/config.md) references.\n"

	claims := ExtractClaimsFromContent(content, "CLAUDE.md")

	// Should NOT extract "util/buildProject.kt" from display text.
	// Should extract "docs/config.md" from the link target (it looks like a local path).
	for _, c := range claims {
		if c.Kind == FilePathClaim && c.Value == "util/buildProject.kt" {
			t.Error("should not extract markdown link display text as file path claim")
		}
	}
}

func TestVerify_SubdirectoryPathSearch(t *testing.T) {
	root := t.TempDir()
	// Create a deeply nested file.
	deep := filepath.Join(root, "pkg", "internal", "shared")
	must(t, os.MkdirAll(deep, 0o755))
	writeFile(t, filepath.Join(deep, "Assert.kt"), "class Assert\n")

	// Claim references abbreviated path "shared/Assert.kt".
	claims := []Claim{
		{Kind: FilePathClaim, Value: "shared/Assert.kt", SourceFile: filepath.Join(root, "CLAUDE.md"), Line: 3},
	}

	findings := Verify(root, claims)
	if len(findings) != 1 || findings[0].Status != Valid {
		detail := ""
		if len(findings) > 0 {
			detail = findings[0].Detail
		}
		t.Errorf("expected valid for abbreviated path 'shared/Assert.kt', got %s: %s",
			findings[0].Status, detail)
	}
}

func TestHasCodeExtension(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"main.go", true},
		{"app.ts", true},
		{"style.css", true},
		{"photo.png", false},
		{"binary.exe", false},
		{"noext", false},
	}
	for _, tt := range tests {
		got := hasCodeExtension(tt.path)
		if got != tt.want {
			t.Errorf("hasCodeExtension(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

// Helpers

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && stringContains(s, substr))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
