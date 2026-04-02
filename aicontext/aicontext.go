// Package aicontext detects and verifies AI context files such as CLAUDE.md,
// AGENTS.md, .cursorrules, and .github/copilot-instructions.md.
//
// These files contain project conventions, file paths, architecture descriptions,
// and symbol references that can become stale as the codebase evolves. This package
// extracts verifiable claims from these files and checks them against the filesystem.
package aicontext

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// KnownFiles lists the well-known AI context file names and paths to search for.
var KnownFiles = []string{
	"CLAUDE.md",
	"AGENTS.md",
	".cursorrules",
	".cursorignore",
	".github/copilot-instructions.md",
	".github/AGENTS.md",
	".windsurfrules",
	".clinerules",
}

// ClaimKind classifies a verifiable claim extracted from an AI context file.
type ClaimKind string

const (
	// FilePathClaim is a reference to a file or directory path.
	FilePathClaim ClaimKind = "file_path"
	// PackageRefClaim is a reference to a Go package or import path.
	PackageRefClaim ClaimKind = "package_ref"
	// CommandClaim is a reference to a CLI command or script.
	CommandClaim ClaimKind = "command"
)

// Claim represents a single verifiable reference extracted from an AI context file.
type Claim struct {
	Kind       ClaimKind
	Value      string // the extracted reference (e.g. "cmd/livedocs/main.go")
	SourceFile string // the AI context file containing this claim
	Line       int    // 1-based line number in the source file
}

// Finding represents the result of verifying a single claim.
type Finding struct {
	Claim  Claim
	Status Status
	Detail string
}

// Status indicates whether a claim is valid or stale.
type Status string

const (
	// Valid means the referenced path or symbol exists.
	Valid Status = "valid"
	// Stale means the referenced path or symbol no longer exists.
	Stale Status = "stale"
)

// Report aggregates verification results for AI context files in a repository.
type Report struct {
	Root        string    // repository root that was scanned
	Files       []string  // AI context files found
	TotalClaims int       // total verifiable claims extracted
	ValidCount  int       // claims that passed verification
	StaleCount  int       // claims that failed verification
	Findings    []Finding // all findings (both valid and stale)
}

// HasDrift returns true if any stale references were found.
func (r *Report) HasDrift() bool {
	return r.StaleCount > 0
}

// StaleFindings returns only the findings with stale status.
func (r *Report) StaleFindings() []Finding {
	var stale []Finding
	for _, f := range r.Findings {
		if f.Status == Stale {
			stale = append(stale, f)
		}
	}
	return stale
}

// Discover searches for AI context files under root and returns their paths.
// It checks both the root directory and common subdirectories.
func Discover(root string) ([]string, error) {
	var found []string
	seen := make(map[string]bool)

	for _, name := range KnownFiles {
		candidate := filepath.Join(root, name)
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			abs, err := filepath.Abs(candidate)
			if err != nil {
				abs = candidate
			}
			if !seen[abs] {
				seen[abs] = true
				found = append(found, abs)
			}
		}
	}

	// Also walk for nested CLAUDE.md files (monorepos often have per-package ones).
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return err
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "vendor" || name == "node_modules" || name == "_output" {
				return filepath.SkipDir
			}
			return nil
		}
		base := info.Name()
		if isAIContextFile(base) {
			abs, err := filepath.Abs(path)
			if err != nil {
				abs = path
			}
			if !seen[abs] {
				seen[abs] = true
				found = append(found, abs)
			}
		}
		return nil
	})
	if err != nil {
		return found, fmt.Errorf("walk %s: %w", root, err)
	}

	sort.Strings(found)
	return found, nil
}

// isAIContextFile returns true if a filename matches a known AI context file pattern.
func isAIContextFile(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case "claude.md", "agents.md", ".cursorrules", ".cursorignore",
		".windsurfrules", ".clinerules", "copilot-instructions.md":
		return true
	}
	return false
}

// filePathRe matches file paths in backticks or after common patterns.
// Matches things like `cmd/livedocs/main.go`, `drift/drift.go`, `./scripts/build.sh`.
var filePathRe = regexp.MustCompile("`([./~]?[a-zA-Z0-9_-]+(?:/[a-zA-Z0-9_.~*-]+)+(?:\\.[a-zA-Z0-9]+)?/?)`")

// barePathRe matches bare file paths (not in backticks) that have extensions.
// More conservative to avoid false positives.
var barePathRe = regexp.MustCompile(`(?:^|\s)([./]?[a-zA-Z0-9_-]+(?:/[a-zA-Z0-9_.-]+)+\.[a-zA-Z0-9]{1,10})(?:\s|$|[,;:)])`)

// goImportRe matches Go import paths like github.com/foo/bar or k8s.io/client-go.
var goImportRe = regexp.MustCompile("`([a-zA-Z][a-zA-Z0-9.-]+\\.[a-zA-Z]{2,}/[a-zA-Z0-9_./-]+)`")

// commandRe matches command invocations in backticks (go test, npm run, etc.).
var commandRe = regexp.MustCompile("`((?:go|npm|make|cargo|pip|yarn|pnpm|python|bash|sh)\\s+[^`]+)`")

// mdLinkRe matches markdown links: [display text](url)
var mdLinkRe = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)

// ExtractClaims parses an AI context file and returns verifiable claims.
func ExtractClaims(filePath string) ([]Claim, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filePath, err)
	}
	return ExtractClaimsFromContent(string(content), filePath), nil
}

// ExtractClaimsFromContent extracts verifiable claims from AI context file content.
func ExtractClaimsFromContent(content string, filePath string) []Claim {
	var claims []Claim
	seen := make(map[string]bool)

	scanner := bufio.NewScanner(strings.NewReader(content))
	inCodeBlock := false
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		// Track fenced code blocks — skip content inside them, but still
		// extract from the fence markers themselves if they contain paths.
		if strings.HasPrefix(trimmed, "```") {
			inCodeBlock = !inCodeBlock
			continue
		}
		if inCodeBlock {
			continue
		}

		// Neutralize markdown link display text to prevent extracting it as
		// a file path. Replace [display text](url) with just the url portion,
		// so the regex only sees the link target.
		processedLine := mdLinkRe.ReplaceAllString(line, "$2")

		// Extract file paths in backticks.
		for _, match := range filePathRe.FindAllStringSubmatch(processedLine, -1) {
			path := match[1]
			if isVerifiablePath(path) {
				key := FilePathClaim.key(path)
				if !seen[key] {
					seen[key] = true
					claims = append(claims, Claim{
						Kind:       FilePathClaim,
						Value:      path,
						SourceFile: filePath,
						Line:       lineNum,
					})
				}
			}
		}

		// Extract bare paths with extensions (more conservative).
		for _, match := range barePathRe.FindAllStringSubmatch(processedLine, -1) {
			path := match[1]
			if isVerifiablePath(path) && hasCodeExtension(path) {
				key := FilePathClaim.key(path)
				if !seen[key] {
					seen[key] = true
					claims = append(claims, Claim{
						Kind:       FilePathClaim,
						Value:      path,
						SourceFile: filePath,
						Line:       lineNum,
					})
				}
			}
		}

		// Extract Go import paths.
		for _, match := range goImportRe.FindAllStringSubmatch(line, -1) {
			importPath := match[1]
			// Skip URLs and common false positives.
			if strings.Contains(importPath, "://") {
				continue
			}
			key := PackageRefClaim.key(importPath)
			if !seen[key] {
				seen[key] = true
				claims = append(claims, Claim{
					Kind:       PackageRefClaim,
					Value:      importPath,
					SourceFile: filePath,
					Line:       lineNum,
				})
			}
		}
	}

	return claims
}

// key creates a dedup key for a claim kind+value pair.
func (k ClaimKind) key(value string) string {
	return string(k) + ":" + value
}

// branchPrefixes lists common git branch naming prefixes that should not be
// treated as file paths.
var branchPrefixes = []string{
	"feature/", "fix/", "bugfix/", "hotfix/", "release/",
	"chore/", "docs/", "refactor/", "perf/", "ci/",
}

// proseSlashPairs lists short slash-separated token pairs that are code/prose
// concepts rather than file paths.
var proseSlashPairs = map[string]bool{
	"if/else": true, "true/false": true, "input/output": true,
	"read/write": true, "get/set": true, "push/pull": true,
	"client/server": true, "src/dst": true, "dx/dy": true,
	"req/res": true, "stdin/stdout": true, "yes/no": true,
	"on/off": true, "open/close": true, "start/stop": true,
}

// isVerifiablePath returns true if a path looks like something we can check
// against the filesystem (not a URL, glob-only pattern, or env var).
func isVerifiablePath(path string) bool {
	// Skip URLs.
	if strings.Contains(path, "://") {
		return false
	}
	// Skip env vars.
	if strings.HasPrefix(path, "$") {
		return false
	}
	// Skip patterns containing glob/wildcard characters anywhere.
	if strings.Contains(path, "*") {
		return false
	}
	// Skip paths containing ellipsis (documentation shorthand).
	if strings.Contains(path, "...") {
		return false
	}
	// Must have at least one slash (otherwise it's just a filename, not a path).
	if !strings.Contains(path, "/") {
		return false
	}
	// Skip very short paths that are likely false positives.
	if len(path) < 3 {
		return false
	}
	// Skip common git branch name prefixes.
	lower := strings.ToLower(path)
	for _, prefix := range branchPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	// Skip known prose/code slash pairs.
	if proseSlashPairs[lower] {
		return false
	}
	return true
}

// hasCodeExtension returns true if a path has a recognizable code file extension.
func hasCodeExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go", ".py", ".ts", ".tsx", ".js", ".jsx", ".rs", ".rb", ".java",
		".kt", ".swift", ".c", ".cpp", ".h", ".hpp", ".cs", ".php",
		".sh", ".bash", ".zsh", ".yaml", ".yml", ".json", ".toml", ".md",
		".sql", ".html", ".css", ".scss", ".proto", ".graphql", ".tf":
		return true
	}
	return false
}

// Verify checks all claims against the filesystem rooted at root.
func Verify(root string, claims []Claim) []Finding {
	var findings []Finding

	for _, claim := range claims {
		switch claim.Kind {
		case FilePathClaim:
			findings = append(findings, verifyFilePath(root, claim))
		case PackageRefClaim:
			findings = append(findings, verifyPackageRef(root, claim))
		default:
			// Skip unverifiable claim kinds.
			findings = append(findings, Finding{
				Claim:  claim,
				Status: Valid,
				Detail: "claim kind not verifiable against filesystem",
			})
		}
	}

	return findings
}

// verifyFilePath checks if a file or directory path exists relative to root.
func verifyFilePath(root string, claim Claim) Finding {
	path := claim.Value

	// Normalize: strip leading ./ or ~/
	path = strings.TrimPrefix(path, "./")

	// Try the path relative to the repo root.
	candidate := filepath.Join(root, path)
	if _, err := os.Stat(candidate); err == nil {
		return Finding{
			Claim:  claim,
			Status: Valid,
			Detail: fmt.Sprintf("path exists: %s", candidate),
		}
	}

	// Try relative to the AI context file's directory.
	dir := filepath.Dir(claim.SourceFile)
	candidate = filepath.Join(dir, path)
	if _, err := os.Stat(candidate); err == nil {
		return Finding{
			Claim:  claim,
			Status: Valid,
			Detail: fmt.Sprintf("path exists relative to context file: %s", candidate),
		}
	}

	// For paths with a trailing slash, try as directory.
	if strings.HasSuffix(claim.Value, "/") {
		candidate = filepath.Join(root, strings.TrimSuffix(path, "/"))
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return Finding{
				Claim:  claim,
				Status: Valid,
				Detail: fmt.Sprintf("directory exists: %s", candidate),
			}
		}
	}

	// Last resort: search for the path suffix anywhere in the tree.
	// This handles abbreviated paths like "shared/Assert.kt" that exist
	// deep in the directory hierarchy.
	suffix := filepath.Clean(strings.TrimSuffix(path, "/"))
	suffixWithSep := string(filepath.Separator) + suffix
	found := false
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "vendor" || name == "node_modules" || name == "_output" {
				return filepath.SkipDir
			}
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return nil
		}
		if strings.HasSuffix(string(filepath.Separator)+rel, suffixWithSep) || rel == suffix {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	if found {
		return Finding{
			Claim:  claim,
			Status: Valid,
			Detail: fmt.Sprintf("path found via subdirectory search: %s", suffix),
		}
	}

	return Finding{
		Claim:  claim,
		Status: Stale,
		Detail: fmt.Sprintf("path not found: %q (checked relative to %s)", claim.Value, root),
	}
}

// verifyPackageRef checks if a Go import path can be resolved.
// For paths that look like they belong to the current module, checks directory existence.
func verifyPackageRef(root string, claim Claim) Finding {
	// Read go.mod to find the module path.
	modPath := filepath.Join(root, "go.mod")
	modContent, err := os.ReadFile(modPath)
	if err != nil {
		// No go.mod — cannot verify Go imports.
		return Finding{
			Claim:  claim,
			Status: Valid,
			Detail: "no go.mod found, skipping package ref verification",
		}
	}

	modulePath := extractModulePath(string(modContent))
	if modulePath == "" {
		return Finding{
			Claim:  claim,
			Status: Valid,
			Detail: "could not parse module path from go.mod",
		}
	}

	// Check if the import path belongs to this module.
	if strings.HasPrefix(claim.Value, modulePath) {
		// Strip module prefix to get the relative directory.
		relDir := strings.TrimPrefix(claim.Value, modulePath)
		relDir = strings.TrimPrefix(relDir, "/")
		if relDir == "" {
			relDir = "."
		}
		candidate := filepath.Join(root, relDir)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return Finding{
				Claim:  claim,
				Status: Valid,
				Detail: fmt.Sprintf("local package directory exists: %s", candidate),
			}
		}
		return Finding{
			Claim:  claim,
			Status: Stale,
			Detail: fmt.Sprintf("local package directory not found: %q (expected at %s)", claim.Value, filepath.Join(root, relDir)),
		}
	}

	// External package — check if it appears in go.mod requires.
	if strings.Contains(string(modContent), claim.Value) {
		return Finding{
			Claim:  claim,
			Status: Valid,
			Detail: "package appears in go.mod",
		}
	}

	// Cannot verify external packages not in go.mod — not necessarily stale.
	return Finding{
		Claim:  claim,
		Status: Valid,
		Detail: "external package, not verified against go.mod",
	}
}

// extractModulePath returns the module path from go.mod content.
func extractModulePath(content string) string {
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

// Check discovers AI context files under root, extracts claims, and verifies them.
// This is the main entry point for the package.
func Check(root string) (*Report, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}

	files, err := Discover(absRoot)
	if err != nil {
		return nil, fmt.Errorf("discover AI context files: %w", err)
	}

	report := &Report{
		Root:  absRoot,
		Files: files,
	}

	if len(files) == 0 {
		return report, nil
	}

	var allClaims []Claim
	for _, f := range files {
		claims, err := ExtractClaims(f)
		if err != nil {
			return nil, fmt.Errorf("extract claims from %s: %w", f, err)
		}
		allClaims = append(allClaims, claims...)
	}

	report.TotalClaims = len(allClaims)
	report.Findings = Verify(absRoot, allClaims)

	for _, f := range report.Findings {
		switch f.Status {
		case Valid:
			report.ValidCount++
		case Stale:
			report.StaleCount++
		}
	}

	return report, nil
}

// FormatReport formats a Report as human-readable text.
func FormatReport(r *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## AI Context Files\n\n")

	if len(r.Files) == 0 {
		fmt.Fprintf(&b, "No AI context files found.\n\n")
		return b.String()
	}

	fmt.Fprintf(&b, "Found %d AI context file(s):\n", len(r.Files))
	for _, f := range r.Files {
		rel, err := filepath.Rel(r.Root, f)
		if err != nil {
			rel = f
		}
		fmt.Fprintf(&b, "- `%s`\n", rel)
	}
	fmt.Fprintf(&b, "\n")

	fmt.Fprintf(&b, "**Claims**: %d total, %d valid, %d stale\n\n",
		r.TotalClaims, r.ValidCount, r.StaleCount)

	stale := r.StaleFindings()
	if len(stale) == 0 {
		fmt.Fprintf(&b, "All references are valid.\n\n")
		return b.String()
	}

	fmt.Fprintf(&b, "### Stale References\n\n")
	for _, f := range stale {
		rel, err := filepath.Rel(r.Root, f.Claim.SourceFile)
		if err != nil {
			rel = f.Claim.SourceFile
		}
		fmt.Fprintf(&b, "- **%s:%d** — `%s` (%s): %s\n",
			rel, f.Claim.Line, f.Claim.Value, f.Claim.Kind, f.Detail)
	}
	fmt.Fprintf(&b, "\n")

	return b.String()
}
