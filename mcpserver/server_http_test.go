package mcpserver

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// getFreePort returns an available TCP port on localhost.
func getFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("get free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

func TestServeHTTP_StartsAndAcceptsSSE(t *testing.T) {
	claimsDB := setupTestDB(t)
	defer claimsDB.Close()

	srv := NewWithDB(claimsDB)
	defer srv.Close()

	port := getFreePort(t)
	addr := fmt.Sprintf(":%d", port)

	// Start the HTTP server in a goroutine.
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ServeHTTP(addr)
	}()

	// Wait for the server to be ready by polling the SSE endpoint.
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	var lastErr error
	for i := 0; i < 50; i++ {
		time.Sleep(50 * time.Millisecond)
		resp, err := http.Get(baseURL + "/sse")
		if err != nil {
			lastErr = err
			continue
		}
		// The SSE endpoint should return 200 with text/event-stream.
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			lastErr = fmt.Errorf("unexpected status: %d", resp.StatusCode)
			continue
		}
		ct := resp.Header.Get("Content-Type")
		if !strings.Contains(ct, "text/event-stream") {
			resp.Body.Close()
			t.Fatalf("expected text/event-stream content type, got %q", ct)
		}

		// Read the first SSE event — should be the endpoint event.
		scanner := bufio.NewScanner(resp.Body)
		var eventLines []string
		for scanner.Scan() {
			line := scanner.Text()
			eventLines = append(eventLines, line)
			// The endpoint event ends with a blank line after data.
			if line == "" && len(eventLines) > 1 {
				break
			}
		}
		resp.Body.Close()

		// Verify we got an endpoint event containing /message.
		joined := strings.Join(eventLines, "\n")
		if !strings.Contains(joined, "endpoint") {
			t.Fatalf("expected endpoint event, got: %s", joined)
		}
		if !strings.Contains(joined, "/message") {
			t.Fatalf("expected /message in endpoint event, got: %s", joined)
		}

		// Success — server is serving SSE correctly.
		return
	}
	t.Fatalf("server did not become ready: %v", lastErr)
}

func TestServeHTTP_MessageEndpointRejectsGET(t *testing.T) {
	claimsDB := setupTestDB(t)
	defer claimsDB.Close()

	srv := NewWithDB(claimsDB)
	defer srv.Close()

	port := getFreePort(t)
	addr := fmt.Sprintf(":%d", port)

	go func() {
		_ = srv.ServeHTTP(addr)
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	// Wait for server to be ready.
	for i := 0; i < 50; i++ {
		time.Sleep(50 * time.Millisecond)
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			conn.Close()
			break
		}
	}

	// GET to /message should return an error (no session ID or wrong method).
	resp, err := http.Get(baseURL + "/message")
	if err != nil {
		t.Fatalf("GET /message: %v", err)
	}
	defer resp.Body.Close()

	// The SSE server returns 400 for invalid requests to /message.
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
		t.Fatalf("expected error status for GET /message, got %d", resp.StatusCode)
	}
}

func TestServeHTTP_NotFoundForUnknownPaths(t *testing.T) {
	claimsDB := setupTestDB(t)
	defer claimsDB.Close()

	srv := NewWithDB(claimsDB)
	defer srv.Close()

	port := getFreePort(t)
	addr := fmt.Sprintf(":%d", port)

	go func() {
		_ = srv.ServeHTTP(addr)
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)

	// Wait for server to be ready.
	for i := 0; i < 50; i++ {
		time.Sleep(50 * time.Millisecond)
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			conn.Close()
			break
		}
	}

	resp, err := http.Get(baseURL + "/nonexistent")
	if err != nil {
		t.Fatalf("GET /nonexistent: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for /nonexistent, got %d", resp.StatusCode)
	}
}
