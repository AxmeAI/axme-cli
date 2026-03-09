package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingSecretStore struct {
	err error
}

func (s *failingSecretStore) Mode() string   { return "keyring" }
func (s *failingSecretStore) Detail() string { return "failing" }
func (s *failingSecretStore) Load(contextName string) (storedContextSecrets, error) {
	return storedContextSecrets{}, s.err
}
func (s *failingSecretStore) Save(contextName string, secrets storedContextSecrets) error {
	return s.err
}

func TestPersistConfigStoresSecretsOutsideConfig(t *testing.T) {
	tempDir := t.TempDir()
	cfgFile := filepath.Join(tempDir, "config.json")
	rt := &runtime{
		cfgFile:     cfgFile,
		secretStore: &fileSecretStore{path: filepath.Join(tempDir, "secrets.json")},
		cfg: &appConfig{
			ActiveContext: "default",
			Contexts: map[string]*clientConfig{
				"default": {
					BaseURL:     "https://api.example.com",
					APIKey:      "workspace-secret",
					ActorToken:  "account-secret",
					OrgID:       "org_123",
					WorkspaceID: "ws_123",
				},
			},
		},
	}

	if err := rt.persistConfig(); err != nil {
		t.Fatalf("persistConfig returned error: %v", err)
	}

	configRaw, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(configRaw), "workspace-secret") || strings.Contains(string(configRaw), "account-secret") {
		t.Fatalf("config.json should not contain secrets, got %s", string(configRaw))
	}

	secretsRaw, err := os.ReadFile(filepath.Join(tempDir, "secrets.json"))
	if err != nil {
		t.Fatalf("read secrets: %v", err)
	}
	var stored map[string]storedContextSecrets
	if err := json.Unmarshal(secretsRaw, &stored); err != nil {
		t.Fatalf("unmarshal secrets: %v", err)
	}
	if stored["default"].APIKey != "workspace-secret" {
		t.Fatalf("expected api key in secret store, got %#v", stored["default"])
	}
	if stored["default"].ActorToken != "account-secret" {
		t.Fatalf("expected actor token in secret store, got %#v", stored["default"])
	}
}

func TestMigratePlaintextSecretsMovesLegacyConfigValues(t *testing.T) {
	tempDir := t.TempDir()
	cfgFile := filepath.Join(tempDir, "config.json")
	rt := &runtime{
		cfgFile:     cfgFile,
		secretStore: &fileSecretStore{path: filepath.Join(tempDir, "secrets.json")},
		cfg: &appConfig{
			ActiveContext: "default",
			Contexts: map[string]*clientConfig{
				"default": {
					BaseURL:    "https://api.example.com",
					APIKey:     "legacy-api-key",
					ActorToken: "legacy-actor-token",
				},
			},
		},
	}

	if err := rt.migratePlaintextSecrets(); err != nil {
		t.Fatalf("migratePlaintextSecrets returned error: %v", err)
	}

	configRaw, err := os.ReadFile(cfgFile)
	if err != nil {
		t.Fatalf("read config after migration: %v", err)
	}
	if strings.Contains(string(configRaw), "legacy-api-key") || strings.Contains(string(configRaw), "legacy-actor-token") {
		t.Fatalf("migrated config should be sanitized, got %s", string(configRaw))
	}

	secrets, err := rt.secretStore.Load("default")
	if err != nil {
		t.Fatalf("load migrated secrets: %v", err)
	}
	if secrets.APIKey != "legacy-api-key" || secrets.ActorToken != "legacy-actor-token" {
		t.Fatalf("unexpected migrated secrets: %#v", secrets)
	}
}

func TestPersistConfigPreservesSecretsForUnloadedContexts(t *testing.T) {
	tempDir := t.TempDir()
	store := &fileSecretStore{path: filepath.Join(tempDir, "secrets.json")}
	if err := store.Save("secondary", storedContextSecrets{
		APIKey:     "secondary-api-key",
		ActorToken: "secondary-actor-token",
	}); err != nil {
		t.Fatalf("seed secret store: %v", err)
	}

	rt := &runtime{
		cfgFile:     filepath.Join(tempDir, "config.json"),
		secretStore: store,
		cfg: &appConfig{
			ActiveContext: "default",
			Contexts: map[string]*clientConfig{
				"default": {
					BaseURL:       "https://api.example.com",
					APIKey:        "default-api-key",
					secretsLoaded: true,
				},
				"secondary": {
					BaseURL: "https://api.example.com",
				},
			},
		},
	}

	if err := rt.persistConfig(); err != nil {
		t.Fatalf("persistConfig returned error: %v", err)
	}

	secrets, err := store.Load("secondary")
	if err != nil {
		t.Fatalf("load preserved secrets: %v", err)
	}
	if secrets.APIKey != "secondary-api-key" || secrets.ActorToken != "secondary-actor-token" {
		t.Fatalf("expected secondary secrets to be preserved, got %#v", secrets)
	}
}

func TestContextWithSecretsSurfacesSecretStoreErrors(t *testing.T) {
	rt := &runtime{
		secretStore: &failingSecretStore{err: errors.New("keyring unavailable")},
		cfg: &appConfig{
			ActiveContext: "default",
			Contexts: map[string]*clientConfig{
				"default": {BaseURL: "https://api.example.com"},
			},
		},
	}

	_, err := rt.contextWithSecrets("default")
	if err == nil || !strings.Contains(err.Error(), "keyring unavailable") {
		t.Fatalf("expected keyring load error, got %v", err)
	}
}

func TestEffectiveContextWithSecretsLoadsStoredValues(t *testing.T) {
	tempDir := t.TempDir()
	store := &fileSecretStore{path: filepath.Join(tempDir, "secrets.json")}
	if err := store.Save("default", storedContextSecrets{
		APIKey:     "stored-api-key",
		ActorToken: "stored-actor-token",
	}); err != nil {
		t.Fatalf("seed secret store: %v", err)
	}

	rt := &runtime{
		secretStore: store,
		cfg: &appConfig{
			ActiveContext: "default",
			Contexts: map[string]*clientConfig{
				"default": {BaseURL: "https://api.example.com"},
			},
		},
	}

	ctx, err := rt.effectiveContextWithSecrets()
	if err != nil {
		t.Fatalf("effectiveContextWithSecrets returned error: %v", err)
	}
	if ctx.APIKey != "stored-api-key" {
		t.Fatalf("expected api key from secret store, got %q", ctx.APIKey)
	}
	if ctx.resolvedActorToken() != "stored-actor-token" {
		t.Fatalf("expected actor token from secret store, got %q", ctx.resolvedActorToken())
	}
}

func TestBuildRootDoesNotExposeAdminCommand(t *testing.T) {
	rt := &runtime{}
	root := buildRoot(rt)

	for _, cmd := range root.Commands() {
		if cmd.Name() == "admin" {
			t.Fatalf("public root command should not expose admin surface")
		}
	}
}

func TestLoginCommandHidesLegacyFlags(t *testing.T) {
	cmd := newLoginCmd(&runtime{})

	for _, name := range []string{"web", "bootstrap-alpha", "onboarding-url"} {
		flag := cmd.Flags().Lookup(name)
		if flag == nil {
			t.Fatalf("expected %q flag to exist", name)
		}
		if !flag.Hidden {
			t.Fatalf("expected %q flag to be hidden", name)
		}
	}

	// --browser is the new visible flag replacing legacy --device
	browserFlag := cmd.Flags().Lookup("browser")
	if browserFlag == nil {
		t.Fatal("expected \"browser\" flag to exist")
	}
	if browserFlag.Hidden {
		t.Fatal("expected \"browser\" flag to be visible (not hidden)")
	}
}

func TestRunDeviceLoginCreatesAndPollsGrantWithoutAPIKey(t *testing.T) {
	tempDir := t.TempDir()
	var createHeader string
	var pollHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/cli-grants":
			createHeader = r.Header.Get("x-api-key")
			_, _ = w.Write([]byte(`{"ok":true,"grant_id":"grant_demo","user_code":"CODE1234","expires_in":30,"poll_interval":1}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/auth/cli-grants/grant_demo":
			pollHeader = r.Header.Get("x-api-key")
			_, _ = w.Write([]byte(`{"ok":true,"state":"approved","api_key":"axme_sa_demo.secret","org_id":"org_demo","workspace_id":"ws_demo"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"not found"}`))
		}
	}))
	defer server.Close()

	store := &fileSecretStore{path: filepath.Join(tempDir, "secrets.json")}
	rt := &runtime{
		cfgFile:     filepath.Join(tempDir, "config.json"),
		httpClient:  server.Client(),
		secretStore: store,
		cfg: &appConfig{
			ActiveContext: "default",
			Contexts: map[string]*clientConfig{
				"default": {BaseURL: server.URL},
			},
		},
	}

	if err := rt.runDeviceLogin(context.Background(), "default", false); err != nil {
		t.Fatalf("runDeviceLogin returned error: %v", err)
	}
	if createHeader != "" {
		t.Fatalf("expected grant creation without x-api-key, got %q", createHeader)
	}
	if pollHeader != "" {
		t.Fatalf("expected grant polling without x-api-key, got %q", pollHeader)
	}
	secrets, err := store.Load("default")
	if err != nil {
		t.Fatalf("load stored secrets: %v", err)
	}
	if secrets.APIKey != "axme_sa_demo.secret" {
		t.Fatalf("expected saved api key, got %#v", secrets)
	}
	if got := rt.cfg.Contexts["default"].OrgID; got != "org_demo" {
		t.Fatalf("expected org_id to be persisted, got %q", got)
	}
	if got := rt.cfg.Contexts["default"].WorkspaceID; got != "ws_demo" {
		t.Fatalf("expected workspace_id to be persisted, got %q", got)
	}
}

func TestPersonalContextFromServerReturnsFriendlyMissingActorTokenMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"missing_actor_token","message":"missing actor token"}}`))
	}))
	defer server.Close()

	rt := &runtime{httpClient: server.Client()}
	cfg := &clientConfig{BaseURL: server.URL, APIKey: "workspace-api-key"}

	_, err := rt.personalContextFromServer(context.Background(), cfg)
	if err == nil {
		t.Fatalf("expected personalContextFromServer to fail")
	}
	cliErr, ok := err.(*cliError)
	if !ok {
		t.Fatalf("expected cliError, got %T", err)
	}
	if !strings.Contains(cliErr.Msg, "requires an account session") {
		t.Fatalf("expected account-session guidance, got %q", cliErr.Msg)
	}
}

func TestPersonalWorkspaceSelectionAPIErrorReturnsFriendlyMembershipMessage(t *testing.T) {
	rt := &runtime{}
	err := rt.personalWorkspaceSelectionAPIError(
		http.StatusForbidden,
		map[string]any{"detail": "workspace selection is outside actor membership scope"},
		`{"detail":"workspace selection is outside actor membership scope"}`,
	)
	if err == nil {
		t.Fatalf("expected error")
	}
	cliErr, ok := err.(*cliError)
	if !ok {
		t.Fatalf("expected cliError, got %T", err)
	}
	if !strings.Contains(cliErr.Msg, "workspace list") {
		t.Fatalf("expected workspace inventory guidance, got %q", cliErr.Msg)
	}
}

func TestWorkspaceUseLoadsStoredSecretsAndPersistsSelection(t *testing.T) {
	tempDir := t.TempDir()
	cfgFile := filepath.Join(tempDir, "config.json")
	store := &fileSecretStore{path: filepath.Join(tempDir, "secrets.json")}
	if err := store.Save("default", storedContextSecrets{
		APIKey:     "stored-api-key",
		ActorToken: "stored-actor-token",
	}); err != nil {
		t.Fatalf("seed secret store: %v", err)
	}

	var sawAPIKey string
	var sawAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAPIKey = r.Header.Get("x-api-key")
		sawAuthorization = r.Header.Get("authorization")
		w.Header().Set("content-type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/portal/personal/context":
			_, _ = w.Write([]byte(`{"ok":true,"workspaces":[{"workspace_id":"ws_stage","org_id":"org_demo","name":"Stage","org_name":"Demo Org","roles":["workspace_admin"]}],"selected_workspace":{"workspace_id":"ws_prod"},"selected_organization":{"org_id":"org_demo"},"context":{"org_id":"org_demo","workspace_id":"ws_prod"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/portal/personal/workspace-selection":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode selection payload: %v", err)
			}
			if asString(payload["workspace_id"]) != "ws_stage" {
				t.Fatalf("expected stage selection payload, got %#v", payload)
			}
			_, _ = w.Write([]byte(`{"ok":true,"context":{"org_id":"org_demo","workspace_id":"ws_stage"},"selected_workspace":{"workspace_id":"ws_stage","org_id":"org_demo","name":"Stage"},"selected_organization":{"org_id":"org_demo","name":"Demo Org"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	rt := &runtime{
		cfgFile:     cfgFile,
		secretStore: store,
		httpClient:  server.Client(),
		outputJSON:  true,
		cfg: &appConfig{
			ActiveContext: "default",
			Contexts: map[string]*clientConfig{
				"default": {BaseURL: server.URL},
			},
		},
	}

	cmd := newWorkspaceCmd(rt)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"use", "Stage"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("workspace use returned error: %v", err)
	}

	if sawAPIKey != "stored-api-key" {
		t.Fatalf("expected api key from secret store, got %q", sawAPIKey)
	}
	if sawAuthorization != "Bearer stored-actor-token" {
		t.Fatalf("expected actor token from secret store, got %q", sawAuthorization)
	}
	if got := rt.cfg.Contexts["default"].OrgID; got != "org_demo" {
		t.Fatalf("expected org_id to persist, got %q", got)
	}
	if got := rt.cfg.Contexts["default"].WorkspaceID; got != "ws_stage" {
		t.Fatalf("expected workspace_id to persist, got %q", got)
	}
}

func TestContextUseHydratesTargetContextFromServerSelection(t *testing.T) {
	tempDir := t.TempDir()
	cfgFile := filepath.Join(tempDir, "config.json")
	store := &fileSecretStore{path: filepath.Join(tempDir, "secrets.json")}
	if err := store.Save("account", storedContextSecrets{
		APIKey:     "account-api-key",
		ActorToken: "account-session-token",
	}); err != nil {
		t.Fatalf("seed secret store: %v", err)
	}

	var sawAPIKey string
	var sawAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAPIKey = r.Header.Get("x-api-key")
		sawAuthorization = r.Header.Get("authorization")
		w.Header().Set("content-type", "application/json")
		if r.Method != http.MethodGet || r.URL.Path != "/v1/portal/personal/context" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"selected_workspace":{"workspace_id":"ws_selected","org_id":"org_selected","name":"Selected"},"selected_organization":{"org_id":"org_selected","name":"Selected Org"},"context":{"org_id":"org_selected","workspace_id":"ws_selected"}}`))
	}))
	defer server.Close()

	rt := &runtime{
		cfgFile:     cfgFile,
		secretStore: store,
		httpClient:  server.Client(),
		outputJSON:  true,
		cfg: &appConfig{
			ActiveContext: "default",
			Contexts: map[string]*clientConfig{
				"default": {BaseURL: "http://unused.example"},
				"account": {BaseURL: server.URL},
			},
		},
	}

	cmd := newContextCmd(rt)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"use", "account"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("context use returned error: %v", err)
	}

	if got := rt.cfg.ActiveContext; got != "account" {
		t.Fatalf("expected active context to switch, got %q", got)
	}
	if sawAPIKey != "account-api-key" {
		t.Fatalf("expected api key from target context secret store, got %q", sawAPIKey)
	}
	if sawAuthorization != "Bearer account-session-token" {
		t.Fatalf("expected actor token from target context secret store, got %q", sawAuthorization)
	}
	if got := rt.cfg.Contexts["account"].OrgID; got != "org_selected" {
		t.Fatalf("expected hydrated org_id, got %q", got)
	}
	if got := rt.cfg.Contexts["account"].WorkspaceID; got != "ws_selected" {
		t.Fatalf("expected hydrated workspace_id, got %q", got)
	}
}

func TestDoctorChecksReportServerPersonalContextAndCacheMisalignment(t *testing.T) {
	tempDir := t.TempDir()
	cfgFile := filepath.Join(tempDir, "config.json")
	if err := os.WriteFile(cfgFile, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}
	store := &fileSecretStore{path: filepath.Join(tempDir, "secrets.json")}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/health":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/auth/sessions":
			_, _ = w.Write([]byte(`{"ok":true,"sessions":[{"session_id":"sess_current","is_current":true,"client_type":"cli"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/portal/personal/context":
			_, _ = w.Write([]byte(`{"ok":true,"workspaces":[{"workspace_id":"ws_server","org_id":"org_server"}],"selected_workspace":{"workspace_id":"ws_server","org_id":"org_server","name":"Server Workspace"},"selected_organization":{"org_id":"org_server","name":"Server Org"},"context":{"org_id":"org_server","workspace_id":"ws_server"},"guidance":{"workspace_count":1}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	rt := &runtime{
		cfgFile:     cfgFile,
		secretStore: store,
		httpClient:  server.Client(),
		cfg:         &appConfig{},
	}
	cfg := &clientConfig{
		BaseURL:     server.URL,
		APIKey:      "workspace-api-key",
		OrgID:       "org_local",
		WorkspaceID: "ws_local",
	}
	cfg.setActorToken("account-session-token")

	checks := rt.doctorChecks(context.Background(), cfg)
	byName := map[string]map[string]any{}
	for _, check := range checks {
		byName[asString(check["check"])] = check
	}

	if !asBool(byName["health_endpoint"]["ok"]) {
		t.Fatalf("expected health_endpoint check to pass, got %#v", byName["health_endpoint"])
	}
	if !asBool(byName["secret_storage"]["ok"]) || !strings.Contains(asString(byName["secret_storage"]["detail"]), "file") {
		t.Fatalf("expected secret_storage to describe file fallback, got %#v", byName["secret_storage"])
	}
	// secret_storage_mode check is present for file stores (renamed from secret_storage_fallback)
	if _, ok := byName["secret_storage_mode"]; !ok {
		t.Fatalf("expected secret_storage_mode check to be present, got keys: %v", func() []string {
			keys := make([]string, 0, len(byName))
			for k := range byName {
				keys = append(keys, k)
			}
			return keys
		}())
	}
	// For an explicit file store (autoFallback=false), mode check is ok=false (user explicitly set it)
	if asBool(byName["secret_storage_mode"]["ok"]) {
		t.Fatalf("expected secret_storage_mode to be warning (ok=false) for explicit file store, got %#v", byName["secret_storage_mode"])
	}
	if got := asString(byName["secret_storage_mode"]["detail"]); !strings.Contains(got, axmeCLISecretStorageEnv+"=file") {
		t.Fatalf("expected file fallback warning detail, got %q", got)
	}
	if !asBool(byName["personal_context"]["ok"]) {
		t.Fatalf("expected personal_context check to pass, got %#v", byName["personal_context"])
	}
	if !asBool(byName["account_session_inventory"]["ok"]) {
		t.Fatalf("expected account_session_inventory check to pass, got %#v", byName["account_session_inventory"])
	}
	if got := asString(byName["account_session_inventory"]["detail"]); !strings.Contains(got, "Current account session: sess_current") {
		t.Fatalf("expected current session detail, got %q", got)
	}
	if got := asString(byName["personal_context"]["detail"]); !strings.Contains(got, "1 visible workspaces") {
		t.Fatalf("expected membership count detail, got %q", got)
	}
	if !asBool(byName["server_selected_workspace"]["ok"]) || asString(byName["server_selected_workspace"]["detail"]) != "ws_server" {
		t.Fatalf("unexpected server_selected_workspace check: %#v", byName["server_selected_workspace"])
	}
	if asBool(byName["workspace_cache_alignment"]["ok"]) {
		t.Fatalf("expected workspace_cache_alignment to report mismatch, got %#v", byName["workspace_cache_alignment"])
	}
	if got := asString(byName["workspace_cache_alignment"]["detail"]); !strings.Contains(got, "org_local") || !strings.Contains(got, "ws_server") {
		t.Fatalf("expected mismatch detail to mention local and server context, got %q", got)
	}
}

func TestPersonalContextSummaryAddsGuidanceForMultipleWorkspaces(t *testing.T) {
	body := map[string]any{
		"organizations": []any{
			map[string]any{"org_id": "org_demo", "name": "Demo Org"},
		},
		"workspaces": []any{
			map[string]any{"workspace_id": "ws_stage", "name": "Stage"},
			map[string]any{"workspace_id": "ws_prod", "name": "Prod"},
		},
		"selected_organization": map[string]any{"org_id": "org_demo", "name": "Demo Org"},
		"selected_workspace":    map[string]any{"workspace_id": "ws_prod", "name": "Prod"},
		"context":               map[string]any{"org_id": "org_demo", "workspace_id": "ws_prod"},
		"guidance":              map[string]any{"workspace_count": 2},
	}

	summary := personalContextSummary(body)
	if got := summary["membership_count"]; got != 2 {
		t.Fatalf("expected membership_count=2, got %#v", got)
	}
	if got := summary["organization_count"]; got != 1 {
		t.Fatalf("expected organization_count=1, got %#v", got)
	}
	if got := asString(summary["guidance_message"]); !strings.Contains(got, "Selected workspace: Prod") || !strings.Contains(got, "workspace use") {
		t.Fatalf("expected workspace switch guidance, got %q", got)
	}
}

func TestAccountSessionSummaryReportsCurrentAndRevokedSessions(t *testing.T) {
	summary := accountSessionSummary([]map[string]any{
		{"session_id": "sess_current", "is_current": true, "client_type": "cli"},
		{"session_id": "sess_other", "is_current": false, "client_type": "web"},
		{"session_id": "sess_revoked", "is_current": false, "revoked_at": "2026-03-08T12:00:00Z"},
	}, true)
	if got := summary["session_count"]; got != 3 {
		t.Fatalf("expected session_count=3, got %#v", got)
	}
	if got := summary["active_count"]; got != 2 {
		t.Fatalf("expected active_count=2, got %#v", got)
	}
	if got := summary["revoked_count"]; got != 1 {
		t.Fatalf("expected revoked_count=1, got %#v", got)
	}
	if !asBool(summary["has_current_session"]) || asString(summary["current_session_id"]) != "sess_current" {
		t.Fatalf("expected current session metadata, got %#v", summary)
	}
	if got := asString(summary["guidance_message"]); !strings.Contains(got, "session revoke <session-id>") {
		t.Fatalf("expected cleanup guidance, got %q", got)
	}
}

func TestLogoutGuidanceMessagePreservesWorkspaceTokenContext(t *testing.T) {
	msg := logoutGuidanceMessage(false, map[string]any{
		"attempted": true,
		"revoked":   true,
		"mode":      "current_session",
	})
	if !strings.Contains(msg, "workspace API key remains available") {
		t.Fatalf("expected workspace-token guidance, got %q", msg)
	}
}

func TestSessionRevokeGuidanceMessageForCurrentSessionMentionsLogout(t *testing.T) {
	msg := sessionRevokeGuidanceMessage("current_session", true)
	if !strings.Contains(msg, "`axme logout`") || !strings.Contains(msg, "`axme login`") {
		t.Fatalf("expected stale-token guidance, got %q", msg)
	}
}

func TestDeviceLoginSummaryIncludesMembershipInventoryAndSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/portal/personal/context" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"organizations":[{"org_id":"org_demo","name":"Demo Org"}],"workspaces":[{"workspace_id":"ws_stage","org_id":"org_demo","name":"Stage"},{"workspace_id":"ws_prod","org_id":"org_demo","name":"Prod"}],"selected_organization":{"org_id":"org_demo","name":"Demo Org"},"selected_workspace":{"workspace_id":"ws_prod","org_id":"org_demo","name":"Prod"},"context":{"org_id":"org_demo","workspace_id":"ws_prod"},"guidance":{"workspace_count":2}}`))
	}))
	defer server.Close()

	rt := &runtime{httpClient: server.Client()}
	cfg := &clientConfig{
		BaseURL:     server.URL,
		APIKey:      "workspace-api-key",
		OrgID:       "org_demo",
		WorkspaceID: "ws_prod",
	}
	cfg.setActorToken("account-session-token")

	summary := rt.deviceLoginSummary(context.Background(), cfg, "default", true)
	if !asBool(summary["ok"]) {
		t.Fatalf("expected ok summary, got %#v", summary)
	}
	if !asBool(summary["has_account_session"]) {
		t.Fatalf("expected account session marker, got %#v", summary)
	}
	if got := asString(asMap(summary["selected_workspace"])["workspace_id"]); got != "ws_prod" {
		t.Fatalf("expected selected workspace in summary, got %#v", summary["selected_workspace"])
	}
	if got := summary["membership_count"]; got != 2 {
		t.Fatalf("expected membership_count=2, got %#v", got)
	}
	if got := summary["organization_count"]; got != 1 {
		t.Fatalf("expected organization_count=1, got %#v", got)
	}
	if got := summary["server_guidance"]; asMap(got)["workspace_count"] != float64(2) && asMap(got)["workspace_count"] != 2 {
		t.Fatalf("expected server guidance to be preserved, got %#v", got)
	}
	if got := asString(summary["guidance_message"]); !strings.Contains(got, "workspace use") {
		t.Fatalf("expected login summary to include switch guidance, got %q", got)
	}
}

func TestResolveEnterpriseWorkspaceContextUsesServerSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/portal/personal/context" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"selected_organization":{"org_id":"org_demo","name":"Demo Org"},"selected_workspace":{"workspace_id":"ws_demo","org_id":"org_demo","name":"Demo Workspace"},"context":{"org_id":"org_demo","workspace_id":"ws_demo"}}`))
	}))
	defer server.Close()

	rt := &runtime{httpClient: server.Client()}
	cfg := &clientConfig{BaseURL: server.URL}
	cfg.setActorToken("account-session-token")

	orgID, workspaceID, err := rt.resolveEnterpriseWorkspaceContext(context.Background(), cfg, "", "")
	if err != nil {
		t.Fatalf("resolveEnterpriseWorkspaceContext returned error: %v", err)
	}
	if orgID != "org_demo" || workspaceID != "ws_demo" {
		t.Fatalf("expected server-selected context, got org=%q workspace=%q", orgID, workspaceID)
	}
}

func TestResolveEnterpriseWorkspaceContextReturnsGuidanceWhenSelectionMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/portal/personal/context" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"organizations":[{"org_id":"org_demo","name":"Demo Org"}],"workspaces":[{"workspace_id":"ws_stage","org_id":"org_demo","name":"Stage"},{"workspace_id":"ws_prod","org_id":"org_demo","name":"Prod"}],"selected_organization":{"org_id":"org_demo","name":"Demo Org"},"context":{"org_id":"org_demo"}}`))
	}))
	defer server.Close()

	rt := &runtime{httpClient: server.Client()}
	cfg := &clientConfig{BaseURL: server.URL}
	cfg.setActorToken("account-session-token")

	_, _, err := rt.resolveEnterpriseWorkspaceContext(context.Background(), cfg, "", "")
	if err == nil {
		t.Fatalf("expected missing workspace selection to fail")
	}
	cliErr, ok := err.(*cliError)
	if !ok {
		t.Fatalf("expected cliError, got %T", err)
	}
	if !strings.Contains(cliErr.Msg, "workspace use") {
		t.Fatalf("expected workspace selection guidance, got %q", cliErr.Msg)
	}
}

func TestMemberScopeSummaryUsesWorkspaceScopeWhenWorkspacePresent(t *testing.T) {
	summary := memberScopeSummary("org_demo", "ws_demo")
	if got := asString(summary["scope"]); got != "workspace" {
		t.Fatalf("expected workspace scope, got %#v", summary)
	}
	if got := asString(summary["description"]); !strings.Contains(got, "workspace-scoped") {
		t.Fatalf("expected workspace-scoped description, got %#v", summary)
	}
}

func TestMemberListGuidanceReferencesSelectedWorkspaceForOrgWideListing(t *testing.T) {
	cfg := &clientConfig{WorkspaceID: "ws_demo"}
	msg := memberListGuidance(cfg, "org_demo", "")
	if !strings.Contains(msg, "organization-wide") || !strings.Contains(msg, "--workspace-id ws_demo") {
		t.Fatalf("expected org-wide listing guidance to reference selected workspace, got %q", msg)
	}
}

func TestWorkspaceListLoadsStoredActorTokenFromSecretStore(t *testing.T) {
	tempDir := t.TempDir()
	cfgFile := filepath.Join(tempDir, "config.json")
	store := &fileSecretStore{path: filepath.Join(tempDir, "secrets.json")}
	if err := store.Save("default", storedContextSecrets{
		APIKey:     "workspace-api-key",
		ActorToken: "stored-actor-token",
	}); err != nil {
		t.Fatalf("seed secret store: %v", err)
	}

	var sawAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuthorization = r.Header.Get("authorization")
		if r.Method != http.MethodGet || r.URL.Path != "/v1/portal/personal/context" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"workspaces":[{"workspace_id":"ws_demo","org_id":"org_demo","name":"Demo"}],"selected_workspace":{"workspace_id":"ws_demo","org_id":"org_demo","name":"Demo"},"selected_organization":{"org_id":"org_demo","name":"Demo Org"},"context":{"org_id":"org_demo","workspace_id":"ws_demo"}}`))
	}))
	defer server.Close()

	rt := &runtime{
		cfgFile:     cfgFile,
		secretStore: store,
		httpClient:  server.Client(),
		outputJSON:  true,
		cfg: &appConfig{
			ActiveContext: "default",
			Contexts: map[string]*clientConfig{
				"default": {BaseURL: server.URL},
			},
		},
	}

	cmd := newWorkspaceCmd(rt)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("workspace list returned error: %v", err)
	}
	if sawAuthorization != "Bearer stored-actor-token" {
		t.Fatalf("expected stored actor token to be loaded, got %q", sawAuthorization)
	}
}

func TestOrgListLoadsStoredActorTokenFromSecretStore(t *testing.T) {
	tempDir := t.TempDir()
	cfgFile := filepath.Join(tempDir, "config.json")
	store := &fileSecretStore{path: filepath.Join(tempDir, "secrets.json")}
	if err := store.Save("default", storedContextSecrets{
		APIKey:     "workspace-api-key",
		ActorToken: "stored-actor-token",
	}); err != nil {
		t.Fatalf("seed secret store: %v", err)
	}

	var sawAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuthorization = r.Header.Get("authorization")
		if r.Method != http.MethodGet || r.URL.Path != "/v1/portal/personal/context" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"organizations":[{"org_id":"org_demo","name":"Demo Org","workspaces":[{"workspace_id":"ws_demo"}]}],"selected_organization":{"org_id":"org_demo","name":"Demo Org"},"context":{"org_id":"org_demo","workspace_id":"ws_demo"}}`))
	}))
	defer server.Close()

	rt := &runtime{
		cfgFile:     cfgFile,
		secretStore: store,
		httpClient:  server.Client(),
		outputJSON:  true,
		cfg: &appConfig{
			ActiveContext: "default",
			Contexts: map[string]*clientConfig{
				"default": {BaseURL: server.URL},
			},
		},
	}

	cmd := newOrgCmd(rt)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("org list returned error: %v", err)
	}
	if sawAuthorization != "Bearer stored-actor-token" {
		t.Fatalf("expected stored actor token to be loaded, got %q", sawAuthorization)
	}
}

func TestServiceAccountsListUsesServerSelectedWorkspaceContext(t *testing.T) {
	tempDir := t.TempDir()
	cfgFile := filepath.Join(tempDir, "config.json")
	store := &fileSecretStore{path: filepath.Join(tempDir, "secrets.json")}
	if err := store.Save("default", storedContextSecrets{
		APIKey:     "workspace-api-key",
		ActorToken: "stored-actor-token",
	}); err != nil {
		t.Fatalf("seed secret store: %v", err)
	}

	var sawAuthorization string
	var sawAPIKey string
	var sawOrgID string
	var sawWorkspaceID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuthorization = r.Header.Get("authorization")
		sawAPIKey = r.Header.Get("x-api-key")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/portal/personal/context":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"selected_organization":{"org_id":"org_demo","name":"Demo Org"},"selected_workspace":{"workspace_id":"ws_demo","org_id":"org_demo","name":"Demo Workspace"},"context":{"org_id":"org_demo","workspace_id":"ws_demo"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/service-accounts":
			sawOrgID = r.URL.Query().Get("org_id")
			sawWorkspaceID = r.URL.Query().Get("workspace_id")
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"service_accounts":[{"service_account_id":"sa_demo","org_id":"org_demo","workspace_id":"ws_demo","name":"Demo Runner","status":"active","created_by_actor_id":"actor_demo","created_at":"2026-03-09T00:00:00Z"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	rt := &runtime{
		cfgFile:     cfgFile,
		secretStore: store,
		httpClient:  server.Client(),
		outputJSON:  true,
		cfg: &appConfig{
			ActiveContext: "default",
			Contexts: map[string]*clientConfig{
				"default": {BaseURL: server.URL},
			},
		},
	}

	cmd := newServiceAccountsCmd(rt)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("service-accounts list returned error: %v", err)
	}
	if sawAuthorization != "Bearer stored-actor-token" {
		t.Fatalf("expected stored actor token to be loaded, got %q", sawAuthorization)
	}
	if sawAPIKey != "workspace-api-key" {
		t.Fatalf("expected stored api key to be loaded, got %q", sawAPIKey)
	}
	if sawOrgID != "org_demo" || sawWorkspaceID != "ws_demo" {
		t.Fatalf("expected server-selected scope, got org=%q workspace=%q", sawOrgID, sawWorkspaceID)
	}
}

func TestKeysListAliasUsesGuidedServiceAccountFlow(t *testing.T) {
	tempDir := t.TempDir()
	cfgFile := filepath.Join(tempDir, "config.json")
	store := &fileSecretStore{path: filepath.Join(tempDir, "secrets.json")}
	if err := store.Save("default", storedContextSecrets{
		APIKey:     "workspace-api-key",
		ActorToken: "stored-actor-token",
	}); err != nil {
		t.Fatalf("seed secret store: %v", err)
	}

	var sawAuthorization string
	var sawAPIKey string
	var sawOrgID string
	var sawWorkspaceID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuthorization = r.Header.Get("authorization")
		sawAPIKey = r.Header.Get("x-api-key")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/portal/personal/context":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"selected_organization":{"org_id":"org_demo","name":"Demo Org"},"selected_workspace":{"workspace_id":"ws_demo","org_id":"org_demo","name":"Demo Workspace"},"context":{"org_id":"org_demo","workspace_id":"ws_demo"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/service-accounts":
			sawOrgID = r.URL.Query().Get("org_id")
			sawWorkspaceID = r.URL.Query().Get("workspace_id")
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"service_accounts":[{"service_account_id":"sa_demo","org_id":"org_demo","workspace_id":"ws_demo","name":"Demo Runner","status":"active","created_by_actor_id":"actor_demo","created_at":"2026-03-09T00:00:00Z"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	rt := &runtime{
		cfgFile:     cfgFile,
		secretStore: store,
		httpClient:  server.Client(),
		outputJSON:  true,
		cfg: &appConfig{
			ActiveContext: "default",
			Contexts: map[string]*clientConfig{
				"default": {BaseURL: server.URL},
			},
		},
	}

	cmd := newKeysCmd(rt)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("keys list returned error: %v", err)
	}
	if sawAuthorization != "Bearer stored-actor-token" {
		t.Fatalf("expected stored actor token to be loaded, got %q", sawAuthorization)
	}
	if sawAPIKey != "workspace-api-key" {
		t.Fatalf("expected stored api key to be loaded, got %q", sawAPIKey)
	}
	if sawOrgID != "org_demo" || sawWorkspaceID != "ws_demo" {
		t.Fatalf("expected server-selected scope, got org=%q workspace=%q", sawOrgID, sawWorkspaceID)
	}
}

func TestServiceAccountsCreateUsesServerSelectedWorkspaceContext(t *testing.T) {
	tempDir := t.TempDir()
	cfgFile := filepath.Join(tempDir, "config.json")
	store := &fileSecretStore{path: filepath.Join(tempDir, "secrets.json")}
	if err := store.Save("default", storedContextSecrets{
		APIKey:     "workspace-api-key",
		ActorToken: "stored-actor-token",
	}); err != nil {
		t.Fatalf("seed secret store: %v", err)
	}

	var sawPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/portal/personal/context":
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"selected_organization":{"org_id":"org_demo","name":"Demo Org"},"selected_workspace":{"workspace_id":"ws_demo","org_id":"org_demo","name":"Demo Workspace"},"context":{"org_id":"org_demo","workspace_id":"ws_demo"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/service-accounts":
			if err := json.NewDecoder(r.Body).Decode(&sawPayload); err != nil {
				t.Fatalf("decode create payload: %v", err)
			}
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"service_account":{"service_account_id":"sa_demo","org_id":"org_demo","workspace_id":"ws_demo","name":"Demo Runner","status":"active","created_by_actor_id":"actor_demo"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	rt := &runtime{
		cfgFile:     cfgFile,
		secretStore: store,
		httpClient:  server.Client(),
		outputJSON:  true,
		cfg: &appConfig{
			ActiveContext: "default",
			Contexts: map[string]*clientConfig{
				"default": {BaseURL: server.URL},
			},
		},
	}

	cmd := newServiceAccountsCmd(rt)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"create", "--name", "Demo Runner"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("service-accounts create returned error: %v", err)
	}
	if asString(sawPayload["org_id"]) != "org_demo" || asString(sawPayload["workspace_id"]) != "ws_demo" {
		t.Fatalf("expected server-selected scope in payload, got %#v", sawPayload)
	}
	if _, exists := sawPayload["created_by_actor_id"]; exists {
		t.Fatalf("did not expect created_by_actor_id in payload, got %#v", sawPayload)
	}
}

func TestSecretStorageFallbackWarningForFileStore(t *testing.T) {
	store := &fileSecretStore{path: "/tmp/axme-secrets.json"}
	got := secretStorageFallbackWarning(store)
	if !strings.Contains(got, axmeCLISecretStorageEnv+"=file") {
		t.Fatalf("expected env var guidance, got %q", got)
	}
	if !strings.Contains(got, store.path) {
		t.Fatalf("expected warning to mention fallback path, got %q", got)
	}
	if !strings.Contains(got, "headless or CI") {
		t.Fatalf("expected warning to mention intended environments, got %q", got)
	}
}

func TestServiceAccountKeysCreateDoesNotRequireCreatedByActorID(t *testing.T) {
	tempDir := t.TempDir()
	cfgFile := filepath.Join(tempDir, "config.json")
	store := &fileSecretStore{path: filepath.Join(tempDir, "secrets.json")}
	if err := store.Save("default", storedContextSecrets{
		APIKey:     "workspace-api-key",
		ActorToken: "stored-actor-token",
	}); err != nil {
		t.Fatalf("seed secret store: %v", err)
	}

	var sawPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/service-accounts/sa_demo/keys":
			if err := json.NewDecoder(r.Body).Decode(&sawPayload); err != nil {
				t.Fatalf("decode key payload: %v", err)
			}
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"key":{"key_id":"sak_demo","service_account_id":"sa_demo","status":"active","token":"axme_sa_sa_demo_secret"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	rt := &runtime{
		cfgFile:     cfgFile,
		secretStore: store,
		httpClient:  server.Client(),
		outputJSON:  true,
		cfg: &appConfig{
			ActiveContext: "default",
			Contexts: map[string]*clientConfig{
				"default": {BaseURL: server.URL},
			},
		},
	}

	cmd := newServiceAccountsCmd(rt)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"keys", "create", "--service-account-id", "sa_demo"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("service-accounts keys create returned error: %v", err)
	}
	if _, exists := sawPayload["created_by_actor_id"]; exists {
		t.Fatalf("did not expect created_by_actor_id in key payload, got %#v", sawPayload)
	}
}

func TestEnterpriseMembersAPIErrorReturnsFriendlyMembershipScopeMessage(t *testing.T) {
	rt := &runtime{}
	err := rt.enterpriseMembersAPIError(
		http.StatusForbidden,
		map[string]any{"detail": "workspace is outside actor membership scope"},
		`{"detail":"workspace is outside actor membership scope"}`,
	)
	if err == nil {
		t.Fatalf("expected error")
	}
	cliErr, ok := err.(*cliError)
	if !ok {
		t.Fatalf("expected cliError, got %T", err)
	}
	if !strings.Contains(cliErr.Msg, "outside your account membership inventory") || !strings.Contains(cliErr.Msg, "workspace list") {
		t.Fatalf("expected membership-scope guidance, got %q", cliErr.Msg)
	}
}

func TestServiceAccountsAPIErrorReturnsFriendlyPlatformKeyMessage(t *testing.T) {
	rt := &runtime{}
	err := rt.serviceAccountsAPIError(
		http.StatusUnauthorized,
		map[string]any{
			"detail": "missing platform api key",
			"error":  map[string]any{"code": "missing_platform_api_key"},
		},
		`{"detail":"missing platform api key"}`,
	)
	if err == nil {
		t.Fatalf("expected error")
	}
	cliErr, ok := err.(*cliError)
	if !ok {
		t.Fatalf("expected cliError, got %T", err)
	}
	if !strings.Contains(cliErr.Msg, "workspace or platform API key") {
		t.Fatalf("expected platform key guidance, got %q", cliErr.Msg)
	}
}

func TestServiceAccountsAPIErrorReturnsFriendlyMembershipScopeMessage(t *testing.T) {
	rt := &runtime{}
	err := rt.serviceAccountsAPIError(
		http.StatusForbidden,
		map[string]any{"detail": "workspace is outside actor membership scope"},
		`{"detail":"workspace is outside actor membership scope"}`,
	)
	if err == nil {
		t.Fatalf("expected error")
	}
	cliErr, ok := err.(*cliError)
	if !ok {
		t.Fatalf("expected cliError, got %T", err)
	}
	if !strings.Contains(cliErr.Msg, "outside your account membership inventory") {
		t.Fatalf("expected membership-scope guidance, got %q", cliErr.Msg)
	}
}

func TestRevokeCurrentAccountSessionRevokesCurrentServerSession(t *testing.T) {
	var sawAuthorization string
	var revokePayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/auth/sessions":
			sawAuthorization = r.Header.Get("authorization")
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"sessions":[{"session_id":"sess_current","is_current":true}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/sessions/revoke":
			if err := json.NewDecoder(r.Body).Decode(&revokePayload); err != nil {
				t.Fatalf("decode revoke payload: %v", err)
			}
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"session_id":"sess_current","revoked":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	rt := &runtime{httpClient: server.Client()}
	cfg := &clientConfig{BaseURL: server.URL}
	cfg.setActorToken("account-session-token")

	sessionID, revoked, err := rt.revokeCurrentAccountSession(context.Background(), cfg)
	if err != nil {
		t.Fatalf("revokeCurrentAccountSession returned error: %v", err)
	}
	if sessionID != "sess_current" {
		t.Fatalf("expected current session id, got %q", sessionID)
	}
	if !revoked {
		t.Fatalf("expected revokeCurrentAccountSession to report revoked")
	}
	if sawAuthorization != "Bearer account-session-token" {
		t.Fatalf("expected authorization header to be forwarded, got %q", sawAuthorization)
	}
	if asString(revokePayload["session_id"]) != "sess_current" {
		t.Fatalf("unexpected revoke payload: %#v", revokePayload)
	}
}

func TestListAccountSessionsIncludesReturnedSessions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/auth/sessions" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("include_revoked"); got != "true" {
			t.Fatalf("expected include_revoked=true, got %q", got)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"sessions":[{"session_id":"sess_a","is_current":true},{"session_id":"sess_b","is_current":false}]}`))
	}))
	defer server.Close()

	rt := &runtime{httpClient: server.Client()}
	cfg := &clientConfig{BaseURL: server.URL}
	cfg.setActorToken("account-session-token")

	sessions, err := rt.listAccountSessions(context.Background(), cfg, true)
	if err != nil {
		t.Fatalf("listAccountSessions returned error: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected two sessions, got %#v", sessions)
	}
	if asString(sessions[0]["session_id"]) != "sess_a" || !asBool(sessions[0]["is_current"]) {
		t.Fatalf("unexpected first session: %#v", sessions[0])
	}
	summary := accountSessionSummary(sessions, true)
	if got := asString(summary["current_session_id"]); got != "sess_a" {
		t.Fatalf("expected current_session_id=sess_a, got %#v", summary)
	}
}

func TestRevokeAccountSessionByIDRevokesRequestedSession(t *testing.T) {
	var revokePayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/auth/sessions/revoke" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&revokePayload); err != nil {
			t.Fatalf("decode revoke payload: %v", err)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"session_id":"sess_target","revoked":true}`))
	}))
	defer server.Close()

	rt := &runtime{httpClient: server.Client()}
	cfg := &clientConfig{BaseURL: server.URL}
	cfg.setActorToken("account-session-token")

	revoked, err := rt.revokeAccountSessionByID(context.Background(), cfg, "sess_target")
	if err != nil {
		t.Fatalf("revokeAccountSessionByID returned error: %v", err)
	}
	if !revoked {
		t.Fatalf("expected revokeAccountSessionByID to report revoked")
	}
	if asString(revokePayload["session_id"]) != "sess_target" {
		t.Fatalf("unexpected revoke payload: %#v", revokePayload)
	}
}

func TestListEnterpriseMembersReturnsMembers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/organizations/org_123/members" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("workspace_id"); got != "ws_123" {
			t.Fatalf("expected workspace_id filter, got %q", got)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"members":[{"member_id":"member_1","actor_id":"actor_1","role":"workspace_admin","status":"active","workspace_id":"ws_123"}]}`))
	}))
	defer server.Close()

	rt := &runtime{httpClient: server.Client()}
	cfg := &clientConfig{BaseURL: server.URL}
	cfg.setActorToken("account-session-token")

	members, err := rt.listEnterpriseMembers(context.Background(), cfg, "org_123", "ws_123")
	if err != nil {
		t.Fatalf("listEnterpriseMembers returned error: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("expected one member, got %#v", members)
	}
	if asString(members[0]["member_id"]) != "member_1" || asString(members[0]["role"]) != "workspace_admin" {
		t.Fatalf("unexpected member payload: %#v", members[0])
	}
}

func TestEnterpriseMembersAPIErrorReturnsFriendly403Message(t *testing.T) {
	rt := &runtime{}
	err := rt.enterpriseMembersAPIError(
		http.StatusForbidden,
		map[string]any{"detail": "operation requires one of roles ['org_owner', 'org_admin']"},
		`{"detail":"forbidden"}`,
	)
	if err == nil {
		t.Fatalf("expected error")
	}
	cliErr, ok := err.(*cliError)
	if !ok {
		t.Fatalf("expected cliError, got %T", err)
	}
	if !strings.Contains(cliErr.Msg, "organization or workspace admin access") {
		t.Fatalf("expected friendly permission message, got %q", cliErr.Msg)
	}
}

func TestLogoutAllAccountSessionsCallsServerEndpoint(t *testing.T) {
	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/auth/logout-all" {
			http.NotFound(w, r)
			return
		}
		requestCount++
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	rt := &runtime{httpClient: server.Client()}
	cfg := &clientConfig{BaseURL: server.URL}
	cfg.setActorToken("account-session-token")

	status, err := rt.logoutAllAccountSessions(context.Background(), cfg)
	if err != nil {
		t.Fatalf("logoutAllAccountSessions returned error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("expected 200 from logoutAllAccountSessions, got %d", status)
	}
	if requestCount != 1 {
		t.Fatalf("expected exactly one logout-all request, got %d", requestCount)
	}
}
