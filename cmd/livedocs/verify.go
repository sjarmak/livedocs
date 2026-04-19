package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sjarmak/livedocs/aicontext"
)

var verifyCmd = &cobra.Command{
	Use:   "verify [path]",
	Short: "Verify accuracy of AI context files",
	Long: `Verify that references in AI context files (CLAUDE.md, .cursorrules, AGENTS.md, etc.)
are accurate and up-to-date.

Pass a single file to verify just that file, or a directory to scan for all AI context files.
Returns exit code 1 if stale references are found.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runVerify,
}

func init() {
	verifyCmd.Flags().Bool("json", false, "output as JSON (shorthand for --format=json)")
	verifyCmd.Flags().String("format", "human", "output format: human, json, or summary")
}

// VerifyReport is the structured output for the verify command.
type VerifyReport struct {
	Root            string       `json:"root"`
	Files           []FileReport `json:"files"`
	TotalClaims     int          `json:"total_claims"`
	ValidCount      int          `json:"valid_count"`
	StaleCount      int          `json:"stale_count"`
	AccuracyPercent float64      `json:"accuracy_percent"`
	Verdict         string       `json:"verdict"`
}

// FileReport contains per-file verification details.
type FileReport struct {
	Path            string         `json:"path"`
	Claims          int            `json:"claims"`
	Valid           int            `json:"valid"`
	Stale           int            `json:"stale"`
	AccuracyPercent float64        `json:"accuracy_percent"`
	StaleRefs       []StaleRefInfo `json:"stale_refs,omitempty"`
}

// StaleRefInfo describes a single stale reference.
type StaleRefInfo struct {
	Line   int    `json:"line"`
	Kind   string `json:"kind"`
	Value  string `json:"value"`
	Detail string `json:"detail"`
}

func runVerify(cmd *cobra.Command, args []string) error {
	defer resetCmdFlags(cmd)

	path := "."
	if len(args) > 0 {
		path = args[0]
	}

	format, _ := cmd.Flags().GetString("format")
	jsonShortcut, _ := cmd.Flags().GetBool("json")
	if jsonShortcut {
		format = "json"
	}

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("cannot access %q: %w", path, err)
	}

	var root string
	var files []string

	if info.IsDir() {
		root, err = filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("resolve path: %w", err)
		}
		files, err = aicontext.Discover(root)
		if err != nil {
			return fmt.Errorf("discover AI context files: %w", err)
		}
	} else {
		// Single file mode: use the file's parent directory as root.
		absFile, err := filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("resolve path: %w", err)
		}
		root = filepath.Dir(absFile)
		files = []string{absFile}
	}

	out := cmd.OutOrStdout()

	if len(files) == 0 {
		switch format {
		case "json":
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			return enc.Encode(VerifyReport{Root: root, Files: []FileReport{}, Verdict: "no files found"})
		default:
			fmt.Fprintln(out, "No AI context files found.")
			return nil
		}
	}

	report := buildVerifyReport(root, files)

	switch format {
	case "json":
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return fmt.Errorf("encode JSON: %w", err)
		}
	case "summary":
		fmt.Fprintln(out, report.summaryLine())
	case "human":
		fmt.Fprint(out, report.humanFormat())
	default:
		return fmt.Errorf("unknown format %q: use \"human\", \"json\", or \"summary\"", format)
	}

	if report.StaleCount > 0 {
		return fmt.Errorf("verification failed: %d stale reference(s) found", report.StaleCount)
	}
	return nil
}

func buildVerifyReport(root string, files []string) VerifyReport {
	report := VerifyReport{Root: root}

	for _, f := range files {
		claims, err := aicontext.ExtractClaims(f)
		if err != nil {
			continue
		}
		findings := aicontext.Verify(root, claims)

		fr := FileReport{
			Path:   relPath(root, f),
			Claims: len(claims),
		}

		for _, finding := range findings {
			switch finding.Status {
			case aicontext.Valid:
				fr.Valid++
			case aicontext.Stale:
				fr.Stale++
				fr.StaleRefs = append(fr.StaleRefs, StaleRefInfo{
					Line:   finding.Claim.Line,
					Kind:   string(finding.Claim.Kind),
					Value:  finding.Claim.Value,
					Detail: finding.Detail,
				})
			}
		}

		fr.AccuracyPercent = accuracy(fr.Valid, fr.Claims)
		report.Files = append(report.Files, fr)
		report.TotalClaims += fr.Claims
		report.ValidCount += fr.Valid
		report.StaleCount += fr.Stale
	}

	report.AccuracyPercent = accuracy(report.ValidCount, report.TotalClaims)
	report.Verdict = verdict(report.StaleCount)
	return report
}

func accuracy(valid, total int) float64 {
	if total == 0 {
		return 100.0
	}
	return float64(valid) / float64(total) * 100.0
}

func verdict(staleCount int) string {
	if staleCount == 0 {
		return "pass"
	}
	return "fail"
}

func relPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}

func (r VerifyReport) summaryLine() string {
	if r.TotalClaims == 0 {
		return fmt.Sprintf("livedocs verify: %d file(s), 0 claims — nothing to check", len(r.Files))
	}
	return fmt.Sprintf("livedocs verify: %.0f%% accurate (%d/%d claims valid across %d file(s)) — %s",
		r.AccuracyPercent, r.ValidCount, r.TotalClaims, len(r.Files), r.Verdict)
}

func (r VerifyReport) humanFormat() string {
	var b strings.Builder

	fmt.Fprintf(&b, "AI Context Verification Report\n")
	fmt.Fprintf(&b, "==============================\n\n")

	for _, f := range r.Files {
		icon := "PASS"
		if f.Stale > 0 {
			icon = "FAIL"
		}
		fmt.Fprintf(&b, "[%s] %s — %.0f%% accurate (%d/%d claims)\n",
			icon, f.Path, f.AccuracyPercent, f.Valid, f.Claims)

		for _, s := range f.StaleRefs {
			fmt.Fprintf(&b, "       line %d: %s `%s`\n", s.Line, s.Kind, s.Value)
			fmt.Fprintf(&b, "               %s\n", s.Detail)
		}
	}

	fmt.Fprintf(&b, "\n%s\n", r.summaryLine())
	return b.String()
}
