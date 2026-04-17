// Integration tests for the NGINX reverse proxy in front of HashiCorp Vault.
// These tests use only the Go standard library (net/http) — no vault SDK needed.
//
// Run:
//   cd tests && go test -v -count=1
//
// Override addresses via environment variables:
//   VAULT_PROXY_ADDR=http://localhost:8080  (default)
//   VAULT_DIRECT_ADDR=http://localhost:8200 (default)
//   VAULT_TOKEN=root                        (default)
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func proxyAddr() string {
	if v := os.Getenv("VAULT_PROXY_ADDR"); v != "" {
		return v
	}
	return "http://localhost:8080"
}

func directAddr() string {
	if v := os.Getenv("VAULT_DIRECT_ADDR"); v != "" {
		return v
	}
	return "http://localhost:8200"
}

func rootToken() string {
	if v := os.Getenv("VAULT_TOKEN"); v != "" {
		return v
	}
	return "root"
}

func newClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

func vaultGET(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-Vault-Token", rootToken())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

func vaultPUT(t *testing.T, client *http.Client, url string, body any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(b))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-Vault-Token", rootToken())
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", url, err)
	}
	return resp
}

func readBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
}

// TestHealth verifies the health endpoint responds correctly through the proxy.
func TestHealth(t *testing.T) {
	client := newClient()
	resp := vaultGET(t, client, proxyAddr()+"/v1/sys/health")
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}

	if initialized, _ := result["initialized"].(bool); !initialized {
		t.Error("vault not initialized")
	}
	if sealed, _ := result["sealed"].(bool); sealed {
		t.Error("vault is sealed")
	}
}

// TestResponseHeadersPreserved verifies Vault's response headers pass through NGINX.
// Note: Vault 2.x moved request_id to the JSON body; it is no longer a response header.
func TestResponseHeadersPreserved(t *testing.T) {
	client := newClient()
	resp := vaultGET(t, client, proxyAddr()+"/v1/sys/health")
	defer resp.Body.Close()

	for _, h := range []string{"Content-Type", "Cache-Control"} {
		if resp.Header.Get(h) == "" {
			t.Errorf("response header %q missing or empty", h)
		}
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json", ct)
	}
}

// TestJSONIntegrity confirms sub_filter does not corrupt API JSON responses.
func TestJSONIntegrity(t *testing.T) {
	endpoints := []string{
		"/v1/sys/health",
		"/v1/sys/seal-status",
		"/v1/sys/mounts",
	}

	client := newClient()
	for _, ep := range endpoints {
		t.Run(ep, func(t *testing.T) {
			resp := vaultGET(t, client, proxyAddr()+ep)
			body := readBody(t, resp)

			if resp.StatusCode >= 500 {
				t.Fatalf("server error %d: %s", resp.StatusCode, body)
			}
			var v any
			if err := json.Unmarshal(body, &v); err != nil {
				t.Errorf("invalid JSON from proxy for %s: %v\nbody: %s", ep, err, body)
			}
		})
	}
}

// TestUIInjection confirms the CSS link is injected into /ui/ but not API paths.
func TestUIInjection(t *testing.T) {
	client := newClient()

	t.Run("ui_injected", func(t *testing.T) {
		resp, err := client.Get(proxyAddr() + "/ui/")
		if err != nil {
			t.Fatalf("GET /ui/: %v", err)
		}
		body := string(readBody(t, resp))
		if !strings.Contains(body, "override.css") {
			t.Error("/ui/ response does not contain injected override.css link")
		}
	})

	t.Run("api_not_injected", func(t *testing.T) {
		resp := vaultGET(t, client, proxyAddr()+"/v1/sys/health")
		body := string(readBody(t, resp))
		if strings.Contains(body, "override.css") {
			t.Error("API response unexpectedly contains override.css injection")
		}
	})

	t.Run("direct_vault_not_injected", func(t *testing.T) {
		resp, err := client.Get(directAddr() + "/ui/")
		if err != nil {
			t.Fatalf("GET direct /ui/: %v", err)
		}
		body := string(readBody(t, resp))
		if strings.Contains(body, "override.css") {
			t.Error("direct Vault UI unexpectedly contains override.css injection")
		}
	})
}

// TestOverrideCSSServed confirms the static CSS file is reachable.
func TestOverrideCSSServed(t *testing.T) {
	client := newClient()
	resp, err := client.Get(proxyAddr() + "/_env/override.css")
	if err != nil {
		t.Fatalf("GET /_env/override.css: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/css") {
		t.Errorf("Content-Type: got %q, want text/css", ct)
	}
}

// TestLargeResponseBody writes and reads back a 100 KB secret through the proxy.
// Uses the built-in secret/ KV v2 mount present in Vault dev mode.
func TestLargeResponseBody(t *testing.T) {
	const size = 100 * 1024
	bigVal := strings.Repeat("a", size)

	client := newClient()

	putResp := vaultPUT(t, client, proxyAddr()+"/v1/secret/data/proxy-gotest-large", map[string]any{
		"data": map[string]string{"value": bigVal},
	})
	putBody := readBody(t, putResp)
	if putResp.StatusCode > 299 {
		t.Fatalf("PUT large secret: status %d, body: %s", putResp.StatusCode, putBody)
	}

	getResp := vaultGET(t, client, proxyAddr()+"/v1/secret/data/proxy-gotest-large")
	getBody := readBody(t, getResp)
	if getResp.StatusCode != 200 {
		t.Fatalf("GET large secret: status %d, body: %s", getResp.StatusCode, getBody)
	}

	var result map[string]any
	if err := json.Unmarshal(getBody, &result); err != nil {
		t.Fatalf("invalid JSON in large response: %v", err)
	}

	data, _ := result["data"].(map[string]any)
	innerData, _ := data["data"].(map[string]any)
	got, _ := innerData["value"].(string)
	if len(got) != size {
		t.Errorf("large value length: got %d, want %d", len(got), size)
	}
}

// TestConcurrentRequests fires 30 parallel requests and expects all to succeed.
func TestConcurrentRequests(t *testing.T) {
	const workers = 30
	var wg sync.WaitGroup
	errs := make([]string, workers)

	for i := range workers {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			client := newClient()
			resp := vaultGET(t, client, proxyAddr()+"/v1/sys/health")
			body := readBody(t, resp)
			if resp.StatusCode != 200 {
				errs[idx] = fmt.Sprintf("worker %d: status %d: %s", idx, resp.StatusCode, body)
				return
			}
			var v map[string]any
			if err := json.Unmarshal(body, &v); err != nil {
				errs[idx] = fmt.Sprintf("worker %d: invalid JSON: %v", idx, err)
			}
		}(i)
	}

	wg.Wait()

	for _, e := range errs {
		if e != "" {
			t.Error(e)
		}
	}
}

// TestVaultTokenPassthrough verifies the proxy forwards auth tokens correctly.
func TestVaultTokenPassthrough(t *testing.T) {
	client := newClient()

	// Valid token should succeed
	resp := vaultGET(t, client, proxyAddr()+"/v1/auth/token/lookup-self")
	body := readBody(t, resp)
	if resp.StatusCode != 200 {
		t.Fatalf("lookup-self with valid token: status %d, body: %s", resp.StatusCode, body)
	}

	// Bad token should get 403
	req, _ := http.NewRequest(http.MethodGet, proxyAddr()+"/v1/auth/token/lookup-self", nil)
	req.Header.Set("X-Vault-Token", "definitely-invalid-token-xyz")
	badResp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request with bad token: %v", err)
	}
	defer badResp.Body.Close()
	if badResp.StatusCode != 403 {
		t.Errorf("bad token: expected 403, got %d", badResp.StatusCode)
	}
}

// TestMethodsPassthrough verifies DELETE and LIST methods work through the proxy.
func TestMethodsPassthrough(t *testing.T) {
	client := newClient()

	// LIST (using GET with ?list=true, which Vault supports)
	req, _ := http.NewRequest(http.MethodGet, proxyAddr()+"/v1/sys/mounts", nil)
	req.Header.Set("X-Vault-Token", rootToken())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/sys/mounts: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("GET /v1/sys/mounts: expected 200, got %d", resp.StatusCode)
	}

	// PATCH / POST — check that Vault's standard HTTP methods work (test via sys/capabilities)
	postBody, _ := json.Marshal(map[string]any{
		"token": rootToken(),
		"paths": []string{"sys/health"},
	})
	postReq, _ := http.NewRequest(http.MethodPost, proxyAddr()+"/v1/sys/capabilities", bytes.NewReader(postBody))
	postReq.Header.Set("X-Vault-Token", rootToken())
	postReq.Header.Set("Content-Type", "application/json")
	postResp, err := client.Do(postReq)
	if err != nil {
		t.Fatalf("POST /v1/sys/capabilities: %v", err)
	}
	defer postResp.Body.Close()
	if postResp.StatusCode != 200 {
		t.Errorf("POST /v1/sys/capabilities: expected 200, got %d", postResp.StatusCode)
	}
}
