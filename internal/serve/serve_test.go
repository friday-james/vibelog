package serve_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cockpit/internal/initcmd"
	"cockpit/internal/serve"
)

func TestHandler_StateEndpoint(t *testing.T) {
	tmp := t.TempDir()
	if err := initcmd.Run(tmp); err != nil {
		t.Fatal(err)
	}
	h, err := serve.Handler(tmp)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/state.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var state struct {
		Anchor     map[string]any `json:"anchor"`
		Iterations []any          `json:"iterations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.Anchor == nil {
		t.Errorf("expected anchor in response")
	}
	if len(state.Iterations) != 1 {
		t.Errorf("expected 1 iteration after init, got %d", len(state.Iterations))
	}
}

func TestHandler_IndexHTML(t *testing.T) {
	tmp := t.TempDir()
	if err := initcmd.Run(tmp); err != nil {
		t.Fatal(err)
	}
	h, err := serve.Handler(tmp)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "COCKPIT") {
		t.Errorf("expected COCKPIT brand in index.html, got first 200 bytes: %q", string(body)[:min(200, len(body))])
	}
}

func TestHandler_UninitializedProject(t *testing.T) {
	tmp := t.TempDir()
	// no init — .sync/ missing
	h, err := serve.Handler(tmp)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/state.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected 500 on uninitialized project, got %d", resp.StatusCode)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
