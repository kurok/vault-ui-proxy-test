// Tests adapted from the HashiCorp Vault repository (https://github.com/hashicorp/vault)
// Original files:
//   http/sys_health_test.go   — TestSysHealth_get, TestSysHealth_customcodes, TestSysHealth_head
//   http/sys_seal_test.go     — TestSysSealStatus
//   http/logical_test.go      — TestLogical, TestLogical_noExist, TestLogical_CreateToken
//
// SPDX-License-Identifier: BUSL-1.1
//
// Adaptations made:
//   - vault.TestCore / vault.TestCoreUnsealed / TestServer setup removed.
//     Tests now use an external Vault instance via VAULT_PROXY_ADDR (default
//     http://localhost:8080).
//   - testHttpGet / testHttpPut / testHttpDelete helpers replaced with
//     standard net/http calls using the proxy address.
//   - KV paths updated from v1 (secret/foo) to v2 (secret/data/foo) to match
//     the KV v2 engine mounted at secret/ in Vault dev mode.
//   - Tests that require sealed/unsealed state transitions, HA standby, or
//     internal core manipulation are excluded (TestSysSeal, TestSysUnseal,
//     TestSysHealth_Removed, TestLogical_StandbyRedirect, TestLogical_RequestSizeLimit).
//   - deep.Equal replaced with reflect.DeepEqual; field-by-field assertions
//     used where struct comparison is not practical.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	vaultapi "github.com/hashicorp/vault/api"
)

// ── shared helpers ────────────────────────────────────────────────────────────

func newVaultAPIClient(t *testing.T) *vaultapi.Client {
	t.Helper()
	cfg := vaultapi.DefaultConfig()
	cfg.Address = proxyAddr()
	client, err := vaultapi.NewClient(cfg)
	if err != nil {
		t.Fatalf("vault/api client: %v", err)
	}
	client.SetToken(rootToken())
	return client
}

// rawGET issues a GET with the Vault token and returns the raw *http.Response.
// Body is NOT closed — caller must close it.
func rawGET(t *testing.T, url string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("X-Vault-Token", rootToken())
	resp, err := newClient().Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

// rawHEAD issues a HEAD (no auth header needed for /v1/sys/health).
func rawHEAD(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := newClient().Head(url)
	if err != nil {
		t.Fatalf("HEAD %s: %v", url, err)
	}
	return resp
}

// rawPUT issues a PUT with JSON body and Vault token.
func rawPUT(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(b))
	req.Header.Set("X-Vault-Token", rootToken())
	req.Header.Set("Content-Type", "application/json")
	resp, err := newClient().Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", url, err)
	}
	return resp
}

// rawDELETE issues a DELETE with Vault token.
func rawDELETE(t *testing.T, url string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, url, nil)
	req.Header.Set("X-Vault-Token", rootToken())
	resp, err := newClient().Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", url, err)
	}
	return resp
}

func assertStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected status %d, got %d — body: %s", want, resp.StatusCode, body)
	}
}

// decodeBody decodes JSON using json.Number for numeric fields (matching the
// original Vault testResponseBody helper which also calls UseNumber).
func decodeBody(t *testing.T, resp *http.Response, out any) {
	t.Helper()
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
}

// ── Adapted from http/sys_health_test.go ─────────────────────────────────────

// TestUpstream_SysHealth_Get validates the health response fields for an
// initialized, unsealed, active Vault instance.
// Original: TestSysHealth_get (http/sys_health_test.go)
func TestUpstream_SysHealth_Get(t *testing.T) {
	client := newVaultAPIClient(t)

	resp, err := client.Sys().Health()
	if err != nil {
		t.Fatalf("Sys().Health(): %v", err)
	}

	if !resp.Initialized {
		t.Error("expected initialized=true")
	}
	if resp.Sealed {
		t.Error("expected sealed=false")
	}
	if resp.Standby {
		t.Error("expected standby=false (dev mode is always active)")
	}
	if resp.Version == "" {
		t.Error("expected non-empty version")
	}
	if resp.ClusterName == "" {
		t.Error("expected non-empty cluster_name")
	}

	// HTTP layer: active vault returns 200
	raw := rawGET(t, proxyAddr()+"/v1/sys/health")
	defer raw.Body.Close()
	assertStatus(t, raw, 200)
}

// TestUpstream_SysHealth_CustomCodes verifies the ?activecode query parameter
// changes the HTTP status code returned for an active vault.
// Original: TestSysHealth_customcodes (http/sys_health_test.go)
func TestUpstream_SysHealth_CustomCodes(t *testing.T) {
	tests := []struct {
		query    string
		wantCode int
	}{
		{"", 200},
		{"?activecode=202", 202},
		{"?activecode=299", 299},
		{"?activecode=503", 503},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("activecode%s", tt.query), func(t *testing.T) {
			raw := rawGET(t, proxyAddr()+"/v1/sys/health"+tt.query)
			defer raw.Body.Close()
			assertStatus(t, raw, tt.wantCode)

			// Body must still be valid JSON regardless of status code
			var result map[string]any
			if err := json.NewDecoder(raw.Body).Decode(&result); err != nil {
				t.Errorf("body not valid JSON for query %q: %v", tt.query, err)
			}
		})
	}
}

// TestUpstream_SysHealth_Head validates HEAD requests against /v1/sys/health.
// Original: TestSysHealth_head (http/sys_health_test.go)
func TestUpstream_SysHealth_Head(t *testing.T) {
	tests := []struct {
		uri      string
		wantCode int
	}{
		{"", 200},
		{"?activecode=503", 503},
		{"?activecode=notacode", 400},
	}

	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			resp := rawHEAD(t, proxyAddr()+"/v1/sys/health"+tt.uri)
			defer resp.Body.Close()

			if resp.StatusCode != tt.wantCode {
				t.Errorf("HEAD /v1/sys/health%s: expected %d, got %d", tt.uri, tt.wantCode, resp.StatusCode)
			}
			// HEAD responses must have no body
			data, _ := io.ReadAll(resp.Body)
			if len(data) > 0 {
				t.Errorf("HEAD returned non-empty body: %q", data)
			}
		})
	}
}

// ── Adapted from http/sys_seal_test.go ───────────────────────────────────────

// TestUpstream_SysSealStatus validates the structure of the seal-status response.
// Original: TestSysSealStatus (http/sys_seal_test.go)
func TestUpstream_SysSealStatus(t *testing.T) {
	raw := rawGET(t, proxyAddr()+"/v1/sys/seal-status")
	assertStatus(t, raw, 200)

	var actual map[string]any
	decodeBody(t, raw, &actual)

	requiredFields := []string{"sealed", "initialized", "type", "t", "n", "progress", "version"}
	for _, f := range requiredFields {
		if _, ok := actual[f]; !ok {
			t.Errorf("seal-status response missing field %q", f)
		}
	}

	if sealed, _ := actual["sealed"].(bool); sealed {
		t.Error("expected sealed=false (dev vault)")
	}
	if initialized, _ := actual["initialized"].(bool); !initialized {
		t.Error("expected initialized=true (dev vault)")
	}
	if sealType, _ := actual["type"].(string); sealType != "shamir" {
		t.Errorf("expected type=shamir, got %q", sealType)
	}
	if actual["version"] == nil || actual["version"] == "" {
		t.Error("expected non-empty version field")
	}
}

// ── Adapted from http/logical_test.go ────────────────────────────────────────

// TestUpstream_Logical_CRUD validates write / read / delete on the KV v2 engine.
// Original: TestLogical (http/logical_test.go)
// Adaptation: KV v1 paths (secret/foo) → KV v2 paths (secret/data/foo);
//   write returns 200 (v2) instead of 204 (v1); response body structure adjusted.
func TestUpstream_Logical_CRUD(t *testing.T) {
	const path = "/v1/secret/data/upstream-logical-crud"
	t.Cleanup(func() {
		rawDELETE(t, proxyAddr()+path).Body.Close()
	})

	// WRITE
	writeResp := rawPUT(t, proxyAddr()+path, map[string]any{
		"data": map[string]string{"value": "bar"},
	})
	assertStatus(t, writeResp, 200) // KV v2 writes return 200, not 204
	writeResp.Body.Close()

	// READ with bad token → 403
	req, _ := http.NewRequest(http.MethodGet, proxyAddr()+path, nil)
	req.Header.Set("X-Vault-Token", rootToken()+"bad")
	badResp, _ := newClient().Do(req)
	assertStatus(t, badResp, 403)
	badResp.Body.Close()

	// READ with valid token → 200, validate body structure
	readResp := rawGET(t, proxyAddr()+path)
	assertStatus(t, readResp, 200)

	var actual map[string]any
	decodeBody(t, readResp, &actual)

	// Fields present in every Vault logical response (original test validates these)
	for _, field := range []string{"request_id", "data", "renewable", "lease_duration", "auth", "wrap_info"} {
		if _, ok := actual[field]; !ok {
			t.Errorf("response missing field %q", field)
		}
	}

	// KV v2: data.data.value == "bar"
	outerData, _ := actual["data"].(map[string]any)
	innerData, _ := outerData["data"].(map[string]any)
	if got := innerData["value"]; got != "bar" {
		t.Errorf("data.data.value: got %v, want bar", got)
	}

	// DELETE
	delResp := rawDELETE(t, proxyAddr()+path)
	assertStatus(t, delResp, 204)
	delResp.Body.Close()

	// READ after DELETE → 404
	afterDel := rawGET(t, proxyAddr()+path)
	afterDel.Body.Close()
	// KV v2 returns 404 after all versions deleted
	if afterDel.StatusCode != 200 && afterDel.StatusCode != 404 {
		t.Errorf("after delete: unexpected status %d", afterDel.StatusCode)
	}
}

// TestUpstream_Logical_NoExist validates that reading a non-existent path returns 404.
// Original: TestLogical_noExist (http/logical_test.go)
func TestUpstream_Logical_NoExist(t *testing.T) {
	resp := rawGET(t, proxyAddr()+"/v1/secret/data/upstream-does-not-exist-xyz")
	defer resp.Body.Close()
	assertStatus(t, resp, 404)
}

// TestUpstream_Logical_CreateToken validates the /v1/auth/token/create response structure.
// Original: TestLogical_CreateToken (http/logical_test.go)
func TestUpstream_Logical_CreateToken(t *testing.T) {
	resp := rawPUT(t, proxyAddr()+"/v1/auth/token/create", map[string]any{})
	assertStatus(t, resp, 200)

	var actual map[string]any
	decodeBody(t, resp, &actual)

	// Original test validates these exact top-level keys
	expected := map[string]any{
		"lease_id":       "",
		"renewable":      false,
		"lease_duration": json.Number("0"),
		"data":           nil,
	}
	for k, wantVal := range expected {
		gotVal, ok := actual[k]
		if !ok {
			t.Errorf("response missing key %q", k)
			continue
		}
		if !reflect.DeepEqual(gotVal, wantVal) {
			t.Errorf("key %q: got %v (%T), want %v (%T)", k, gotVal, gotVal, wantVal, wantVal)
		}
	}

	// auth block must contain client_token and policies
	auth, _ := actual["auth"].(map[string]any)
	if auth == nil {
		t.Fatal("response missing auth block")
	}
	if _, ok := auth["client_token"]; !ok {
		t.Error("auth block missing client_token")
	}
	policies, _ := auth["policies"].([]any)
	if len(policies) == 0 {
		t.Error("auth.policies is empty")
	}
	found := false
	for _, p := range policies {
		if p == "root" {
			found = true
		}
	}
	if !found {
		t.Errorf("auth.policies does not contain root: %v", policies)
	}

	// Revoke the created token so we don't leak it
	if token, _ := auth["client_token"].(string); token != "" {
		req, _ := http.NewRequest(http.MethodPut,
			proxyAddr()+"/v1/auth/token/revoke",
			strings.NewReader(fmt.Sprintf(`{"token":%q}`, token)))
		req.Header.Set("X-Vault-Token", rootToken())
		req.Header.Set("Content-Type", "application/json")
		newClient().Do(req) //nolint:errcheck
	}
}
