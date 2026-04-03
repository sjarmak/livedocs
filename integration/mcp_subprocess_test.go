//go:build integration

package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/live-docs/live_docs/db"
	_ "modernc.org/sqlite"
)

// TestMCPSubprocess spawns the livedocs binary with the mcp subcommand,
// sends JSON-RPC initialize and tools/list requests over stdin, and
// validates the responses on stdout.
func TestMCPSubprocess(t *testing.T) {
	// 1. Build the livedocs binary.
	bin := buildLivedocs(t)

	// 2. Create a temp data-dir with a small test .claims.db.
	dataDir := t.TempDir()
	seedTestClaimsDB(t, dataDir, "testrepo")

	// 3. Start the subprocess.
	cmd := exec.Command(bin, "mcp", "--data-dir", dataDir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("start subprocess: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		// Give the process a moment to exit cleanly, then kill.
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
		}
	})

	scanner := bufio.NewScanner(stdout)
	// Increase scanner buffer for potentially large JSON responses.
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)

	// 4. Send JSON-RPC initialize request.
	initReq := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0.1.0"}}}` + "\n"
	if _, err := fmt.Fprint(stdin, initReq); err != nil {
		t.Fatalf("write initialize request: %v", err)
	}

	// 5. Read and validate initialize response.
	initResp := readJSONRPCResponse(t, scanner)
	assertJSONRPCID(t, initResp, 1)

	result, ok := initResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize response missing result object: %v", initResp)
	}

	// Validate serverInfo is present.
	serverInfo, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatalf("initialize response missing serverInfo: %v", result)
	}
	if name, _ := serverInfo["name"].(string); name == "" {
		t.Errorf("serverInfo.name is empty")
	}
	t.Logf("serverInfo: %v", serverInfo)

	// Validate capabilities is present.
	if _, ok := result["capabilities"].(map[string]any); !ok {
		t.Fatalf("initialize response missing capabilities: %v", result)
	}

	// 6. Send initialized notification (required by MCP protocol before tools/list).
	initializedNotif := `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n"
	if _, err := fmt.Fprint(stdin, initializedNotif); err != nil {
		t.Fatalf("write initialized notification: %v", err)
	}

	// 7. Send JSON-RPC tools/list request.
	toolsReq := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n"
	if _, err := fmt.Fprint(stdin, toolsReq); err != nil {
		t.Fatalf("write tools/list request: %v", err)
	}

	// 8. Read and validate tools/list response.
	toolsResp := readJSONRPCResponse(t, scanner)
	assertJSONRPCID(t, toolsResp, 2)

	toolsResult, ok := toolsResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list response missing result object: %v", toolsResp)
	}

	toolsRaw, ok := toolsResult["tools"].([]any)
	if !ok {
		t.Fatalf("tools/list response missing tools array: %v", toolsResult)
	}

	toolNames := make(map[string]bool)
	for _, raw := range toolsRaw {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if name, ok := tool["name"].(string); ok {
			toolNames[name] = true
		}
	}

	t.Logf("tools found: %v", toolNames)

	requiredTools := []string{"list_repos", "list_packages", "describe_package", "search_symbols"}
	for _, name := range requiredTools {
		if !toolNames[name] {
			t.Errorf("expected tool %q not found in tools/list response", name)
		}
	}
}

// readJSONRPCResponse reads the next line from scanner and parses it as a
// JSON-RPC response. It fatals on timeout or parse errors.
func readJSONRPCResponse(t *testing.T, scanner *bufio.Scanner) map[string]any {
	t.Helper()

	type scanResult struct {
		line string
		ok   bool
	}
	ch := make(chan scanResult, 1)
	go func() {
		ok := scanner.Scan()
		ch <- scanResult{line: scanner.Text(), ok: ok}
	}()

	select {
	case res := <-ch:
		if !res.ok {
			if err := scanner.Err(); err != nil {
				t.Fatalf("scanner error: %v", err)
			}
			t.Fatal("unexpected EOF from subprocess stdout")
		}
		var resp map[string]any
		if err := json.Unmarshal([]byte(res.line), &resp); err != nil {
			t.Fatalf("parse JSON-RPC response: %v\nraw: %s", err, res.line)
		}
		if resp["jsonrpc"] != "2.0" {
			t.Errorf("expected jsonrpc 2.0, got %v", resp["jsonrpc"])
		}
		return resp
	case <-time.After(30 * time.Second):
		t.Fatal("timeout waiting for JSON-RPC response from subprocess")
		return nil
	}
}

// assertJSONRPCID checks that the response has the expected id field.
func assertJSONRPCID(t *testing.T, resp map[string]any, expectedID float64) {
	t.Helper()
	id, ok := resp["id"].(float64)
	if !ok {
		t.Fatalf("response missing or non-numeric id field: %v", resp)
	}
	if id != expectedID {
		t.Errorf("expected id=%v, got %v", expectedID, id)
	}
	if errField, ok := resp["error"]; ok {
		t.Fatalf("JSON-RPC error in response: %v", errField)
	}
}

// seedTestClaimsDB creates a small .claims.db in dataDir with one test symbol
// and one claim, using the naming convention expected by DBPool (<name>.claims.db).
func seedTestClaimsDB(t *testing.T, dataDir, repoName string) {
	t.Helper()
	dbPath := filepath.Join(dataDir, repoName+".claims.db")

	cdb, err := db.OpenClaimsDB(dbPath)
	if err != nil {
		t.Fatalf("open test claims db: %v", err)
	}
	defer cdb.Close()

	if err := cdb.CreateSchema(); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	symID, err := cdb.UpsertSymbol(db.Symbol{
		Repo:       repoName,
		ImportPath: "pkg/server",
		SymbolName: "NewServer",
		Language:   "go",
		Kind:       "function",
		Visibility: "public",
	})
	if err != nil {
		t.Fatalf("upsert symbol: %v", err)
	}

	_, err = cdb.InsertClaim(db.Claim{
		SubjectID:        symID,
		Predicate:        "defines",
		ObjectText:       "creates a new server instance",
		SourceFile:       "server.go",
		SourceLine:       42,
		Confidence:       1.0,
		ClaimTier:        "structural",
		Extractor:        "test",
		ExtractorVersion: "0.0.1",
		LastVerified:     time.Now().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("insert claim: %v", err)
	}
}
