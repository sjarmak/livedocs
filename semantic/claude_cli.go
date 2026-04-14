package semantic

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ClaudeCLIClient implements LLMClient by shelling out to the `claude` CLI
// in pipe mode (-p). Uses the CLI's existing OAuth authentication — no
// separate API key needed.
type ClaudeCLIClient struct {
	// Model to use (e.g. "haiku", "sonnet"). Empty uses CLI default.
	Model string
}

// NewClaudeCLIClient creates a client that uses the claude CLI.
// Returns an error if the claude binary is not found on PATH.
func NewClaudeCLIClient(model string) (*ClaudeCLIClient, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return nil, fmt.Errorf("claude CLI not found on PATH: %w", err)
	}
	return &ClaudeCLIClient{Model: model}, nil
}

// Complete sends a system+user prompt to the claude CLI and returns the text response.
func (c *ClaudeCLIClient) Complete(ctx context.Context, system, user string) (string, error) {
	args := []string{
		"-p",
		"--output-format", "text",
		"--system-prompt", system,
	}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	args = append(args, user)

	cmd := exec.CommandContext(ctx, "claude", args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("claude CLI exited %d: %s", exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return "", fmt.Errorf("claude CLI: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
