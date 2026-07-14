package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	server "github.com/Zleap-AI/Agent-Bridge/internal/server"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/apierror"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/caller"
)

func TestOwnerCookieOriginProtectionAndAPIKeyCompatibility(t *testing.T) {
	app, httpServer, ownerClient := newInitializedTestServer(t)
	defer app.Close()
	defer httpServer.Close()

	response := requestJSONWithHeaders(t, ownerClient, http.MethodPost, httpServer.URL+"/api/v1/admin/pairing-codes", "", map[string]any{}, map[string]string{
		"Origin": httpServer.URL,
	})
	assertStatus(t, response, http.StatusCreated)
	response.Body.Close()

	response = requestJSONWithHeaders(t, ownerClient, http.MethodPost, httpServer.URL+"/api/v1/admin/pairing-codes", "", map[string]any{}, map[string]string{
		"Origin": "https://attacker.example",
	})
	assertStructuredError(t, response, http.StatusForbidden, apierror.CodeForbidden)

	response = requestJSONWithHeaders(t, ownerClient, http.MethodPost, httpServer.URL+"/api/v1/admin/pairing-codes", "", map[string]any{}, map[string]string{
		"Sec-Fetch-Site": "cross-site",
	})
	assertStructuredError(t, response, http.StatusForbidden, apierror.CodeForbidden)

	// Command-line and other non-browser clients do not send Origin.
	response = requestJSON(t, ownerClient, http.MethodPost, httpServer.URL+"/api/v1/admin/pairing-codes", "", map[string]any{})
	assertStatus(t, response, http.StatusCreated)
	response.Body.Close()

	response = requestJSON(t, ownerClient, http.MethodPost, httpServer.URL+"/api/v1/admin/api-keys", "", map[string]string{"name": "Origin test"})
	assertStatus(t, response, http.StatusCreated)
	var created struct {
		APIKey struct {
			Key string `json:"key"`
		} `json:"api_key"`
	}
	decodeResponse(t, response, &created)
	if created.APIKey.Key == "" {
		t.Fatal("API key was not returned")
	}

	// A valid Caller API key is not subject to browser cookie CSRF checks, even
	// when the same client also has an Owner cookie.
	response = requestJSONWithHeaders(t, ownerClient, http.MethodPost, httpServer.URL+"/api/v1/devices/missing/agents/codex/sessions", created.APIKey.Key, map[string]any{}, map[string]string{
		"Origin": "https://attacker.example",
	})
	assertStructuredError(t, response, http.StatusNotFound, apierror.CodeDeviceNotFound)
}

func TestMessageSizeBoundariesReturnStructuredPayloadErrors(t *testing.T) {
	app, httpServer, ownerClient := newInitializedTestServer(t)
	defer app.Close()
	defer httpServer.Close()

	response := requestJSON(t, ownerClient, http.MethodPost, httpServer.URL+"/api/v1/admin/api-keys", "", map[string]string{"name": "Size test"})
	assertStatus(t, response, http.StatusCreated)
	var created struct {
		APIKey struct {
			Key string `json:"key"`
		} `json:"api_key"`
	}
	decodeResponse(t, response, &created)
	target := httpServer.URL + "/api/v1/devices/missing/agents/codex/sessions/session/messages"

	// The exact public text limit passes validation and reaches target lookup.
	response = requestJSON(t, http.DefaultClient, http.MethodPost, target, created.APIKey.Key, map[string]any{
		"content": []map[string]string{{"type": "text", "text": strings.Repeat("x", caller.MaxMessageTextBytes)}},
	})
	assertStructuredError(t, response, http.StatusNotFound, apierror.CodeDeviceNotFound)

	response = requestJSON(t, http.DefaultClient, http.MethodPost, target, created.APIKey.Key, map[string]any{
		"content": []map[string]string{{"type": "text", "text": strings.Repeat("x", caller.MaxMessageTextBytes+1)}},
	})
	assertStructuredError(t, response, http.StatusRequestEntityTooLarge, apierror.CodePayloadTooLarge)

	// Oversized raw JSON bodies are also reported as structured 413 responses,
	// rather than being collapsed into a generic malformed-JSON error.
	raw := "{\"content\":[{\"type\":\"text\",\"text\":\"" + strings.Repeat("x", 1024*1024) + "\"}]}"
	request, err := http.NewRequest(http.MethodPost, target, strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+created.APIKey.Key)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	assertStructuredError(t, response, http.StatusRequestEntityTooLarge, apierror.CodePayloadTooLarge)

	// Exercise the streaming limit too, where Content-Length is not available.
	request, err = http.NewRequest(http.MethodPost, target, struct{ io.Reader }{strings.NewReader(raw)})
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+created.APIKey.Key)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	assertStructuredError(t, response, http.StatusRequestEntityTooLarge, apierror.CodePayloadTooLarge)
}

func TestCallerMessagePaginationValidation(t *testing.T) {
	app, httpServer, ownerClient := newInitializedTestServer(t)
	defer app.Close()
	defer httpServer.Close()

	base := httpServer.URL + "/api/v1/devices/missing/agents/codex/sessions/session/messages"
	for _, query := range []string{
		"?cursor=-1",
		"?cursor=not-a-number",
		"?cursor=1&cursor=2",
		"?limit=0",
		fmt.Sprintf("?limit=%d", caller.MaxMessagesPageSize+1),
	} {
		response := requestJSON(t, ownerClient, http.MethodGet, base+query, "", nil)
		assertStructuredError(t, response, http.StatusBadRequest, apierror.CodeInvalidRequest)
	}

	// Valid defaults and page values pass validation and reach target lookup.
	for _, query := range []string{"", "?cursor=10&limit=25"} {
		response := requestJSON(t, ownerClient, http.MethodGet, base+query, "", nil)
		assertStructuredError(t, response, http.StatusNotFound, apierror.CodeDeviceNotFound)
	}
}

func TestAPIRoutingErrorsUseJSONEnvelope(t *testing.T) {
	app, httpServer, _ := newInitializedTestServer(t)
	defer app.Close()
	defer httpServer.Close()

	response := requestJSON(t, http.DefaultClient, http.MethodGet, httpServer.URL+"/api/v1/does-not-exist", "", nil)
	assertStructuredError(t, response, http.StatusNotFound, apierror.CodeNotFound)

	response = requestJSON(t, http.DefaultClient, http.MethodPut, httpServer.URL+"/api/v1/status", "", nil)
	if allow := response.Header.Get("Allow"); !strings.Contains(allow, http.MethodGet) || !strings.Contains(allow, http.MethodHead) {
		response.Body.Close()
		t.Fatalf("Allow = %q, want GET and HEAD", allow)
	}
	assertStructuredError(t, response, http.StatusMethodNotAllowed, apierror.CodeMethodNotAllowed)
}

func newInitializedTestServer(t *testing.T) (*server.App, *httptest.Server, *http.Client) {
	t.Helper()
	database := filepath.Join(t.TempDir(), "state.db")
	app, err := server.New(context.Background(), server.Config{
		ListenAddr: "127.0.0.1:0", DataDir: filepath.Dir(database), DatabasePath: database, Version: server.Version,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(app.Handler())
	ownerClient := newCookieClient(t)
	response := requestJSON(t, ownerClient, http.MethodPost, httpServer.URL+"/api/v1/setup", app.SetupToken(), map[string]any{"password": "owner password"})
	assertStatus(t, response, http.StatusCreated)
	response.Body.Close()
	response = requestJSON(t, ownerClient, http.MethodPost, httpServer.URL+"/api/v1/auth/login", "", map[string]any{"password": "owner password"})
	assertStatus(t, response, http.StatusOK)
	response.Body.Close()
	return app, httpServer, ownerClient
}

func requestJSONWithHeaders(t *testing.T, client *http.Client, method, target, bearer string, body any, headers map[string]string) *http.Response {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(method, target, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func assertStructuredError(t *testing.T, response *http.Response, status int, code string) {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("HTTP status = %d, want %d, body=%s", response.StatusCode, status, body)
	}
	var result struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode structured error: %v, body=%s", err, body)
	}
	if result.Error.Code != code {
		t.Fatalf("error code = %q, want %q, body=%s", result.Error.Code, code, body)
	}
}
