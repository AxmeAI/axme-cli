package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------------------------------------------------------------------------
// axme login — smart refresh logic
//
// These tests verify that runEmailLogin is NOT called when a valid
// refresh_token is available, and IS called when the token is expired.
// We test through the tryRefreshActorToken helper which is the core of the
// silent-refresh path in newLoginCmd.
// ---------------------------------------------------------------------------

// mockRefreshServer builds a test HTTP server that handles POST /v1/auth/refresh.
// If succeed=true it returns a valid new actor_token; otherwise returns 401.
func mockRefreshServer(t *testing.T, succeed bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/auth/refresh" {
			http.NotFound(w, r)
			return
		}
		if succeed {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":                   true,
				"access_token":         "new-actor-token-abc123",
				"account_session_token": "new-actor-token-abc123",
				"expires_in":           3600,
			})
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":    false,
				"code":  "token_expired",
				"error": "refresh token expired",
			})
		}
	}))
}

// TestLoginRefresh_ValidToken verifies that tryRefreshActorToken returns a new
// token when the server accepts the refresh_token.
func TestLoginRefresh_ValidToken(t *testing.T) {
	srv := mockRefreshServer(t, true)
	defer srv.Close()

	rt := &runtime{
		cfg: &appConfig{
			ActiveContext: "default",
			Contexts: map[string]*clientConfig{
				"default": {BaseURL: srv.URL},
			},
		},
		httpClient:   &http.Client{},
		streamClient: &http.Client{},
	}

	c := rt.ensureContext("default")
	c.RefreshToken = "valid-refresh-token-xyz"

	newToken, err := rt.tryRefreshActorToken(context.Background(), c)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if newToken == "" {
		t.Fatal("expected a non-empty new actor token")
	}
	if c.resolvedActorToken() == "" {
		t.Fatal("expected actor token to be set on context after refresh")
	}
}

// TestLoginRefresh_ExpiredToken verifies that tryRefreshActorToken returns an
// error (and empty token) when the server rejects the refresh_token with 401.
func TestLoginRefresh_ExpiredToken(t *testing.T) {
	srv := mockRefreshServer(t, false)
	defer srv.Close()

	rt := &runtime{
		cfg: &appConfig{
			ActiveContext: "default",
			Contexts: map[string]*clientConfig{
				"default": {BaseURL: srv.URL},
			},
		},
		httpClient:   &http.Client{},
		streamClient: &http.Client{},
	}

	c := rt.ensureContext("default")
	c.RefreshToken = "expired-refresh-token"

	newToken, err := rt.tryRefreshActorToken(context.Background(), c)
	if err == nil {
		t.Fatal("expected an error for expired refresh token, got nil")
	}
	if newToken != "" {
		t.Fatalf("expected empty token on failure, got: %s", newToken)
	}
}

// TestLoginRefresh_NoRefreshToken verifies that tryRefreshActorToken returns
// an error immediately when RefreshToken is empty (no server call made).
func TestLoginRefresh_NoRefreshToken(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		http.NotFound(w, r)
	}))
	defer srv.Close()

	rt := &runtime{
		cfg: &appConfig{
			ActiveContext: "default",
			Contexts: map[string]*clientConfig{
				"default": {BaseURL: srv.URL},
			},
		},
		httpClient:   &http.Client{},
		streamClient: &http.Client{},
	}

	c := rt.ensureContext("default")
	c.RefreshToken = "" // no token

	newToken, err := rt.tryRefreshActorToken(context.Background(), c)
	if err == nil {
		t.Fatal("expected error when no refresh token, got nil")
	}
	if newToken != "" {
		t.Fatalf("expected empty token, got: %s", newToken)
	}
	if callCount > 0 {
		t.Errorf("expected no HTTP calls when refresh token is empty, got %d", callCount)
	}
}

// ---------------------------------------------------------------------------
// scenario-agents store helpers
// ---------------------------------------------------------------------------

func TestScenarioAgentsStore_UpsertAndLoad(t *testing.T) {
	store := scenarioAgentsStore{}

	creds1 := scenarioAgentCreds{
		Address:          "agent://acme/prod/approver",
		ServiceAccountID: "sa_abc123",
		KeyID:            "sak_xyz",
		APIKey:           "axme_sa_sa_abc123_secret1",
		CreatedAt:        "2026-03-13T10:00:00Z",
	}
	upsertScenarioAgent(&store, creds1)

	if len(store.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(store.Agents))
	}
	if store.Agents[0].APIKey != creds1.APIKey {
		t.Errorf("wrong api_key: got %s", store.Agents[0].APIKey)
	}

	// Upsert with same address — should update, not append
	creds1updated := creds1
	creds1updated.APIKey = "axme_sa_sa_abc123_secret2"
	upsertScenarioAgent(&store, creds1updated)

	if len(store.Agents) != 1 {
		t.Fatalf("expected still 1 agent after update, got %d", len(store.Agents))
	}
	if store.Agents[0].APIKey != "axme_sa_sa_abc123_secret2" {
		t.Errorf("expected updated api_key, got %s", store.Agents[0].APIKey)
	}

	// Upsert a second agent
	creds2 := scenarioAgentCreds{Address: "agent://acme/prod/reviewer", ServiceAccountID: "sa_def456"}
	upsertScenarioAgent(&store, creds2)
	if len(store.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(store.Agents))
	}
}

func TestScenarioAgentsStore_JSONRoundtrip(t *testing.T) {
	original := scenarioAgentsStore{
		Agents: []scenarioAgentCreds{
			{Address: "agent://x/y/z", ServiceAccountID: "sa_1", KeyID: "sak_1", APIKey: "tok1", CreatedAt: "2026-03-13T00:00:00Z"},
		},
	}
	data, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var restored scenarioAgentsStore
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(restored.Agents) != 1 {
		t.Fatalf("expected 1 agent after roundtrip, got %d", len(restored.Agents))
	}
	if restored.Agents[0].APIKey != "tok1" {
		t.Errorf("wrong api_key after roundtrip: %s", restored.Agents[0].APIKey)
	}
}

// ---------------------------------------------------------------------------
// fileStoreNoticeSeen in appConfig
// ---------------------------------------------------------------------------

func TestAppConfig_FileStoreNoticeSeen_Serialisation(t *testing.T) {
	cfg := appConfig{
		ActiveContext:        "default",
		Contexts:             map[string]*clientConfig{"default": {BaseURL: "http://localhost"}},
		FileStoreNoticeSeen:  true,
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	val, ok := out["file_store_notice_seen"]
	if !ok {
		t.Fatal("file_store_notice_seen missing from JSON output")
	}
	if val != true {
		t.Errorf("expected true, got %v", val)
	}
}

func TestAppConfig_FileStoreNoticeSeen_OmitWhenFalse(t *testing.T) {
	cfg := appConfig{
		ActiveContext: "default",
		Contexts:      map[string]*clientConfig{"default": {BaseURL: "http://localhost"}},
		// FileStoreNoticeSeen is false (zero value) → omitempty should omit it
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := out["file_store_notice_seen"]; ok {
		t.Error("file_store_notice_seen should be omitted when false")
	}
}

// Ensure the test file compiles (uses fmt to avoid unused import errors in isolation).
var _ = fmt.Sprintf
