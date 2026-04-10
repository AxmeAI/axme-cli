package main

import (
	"os"
	"strings"
	"testing"
)

// resolveDashboardURL precedence:
//  1. explicit --dashboard-url flag overrides everything
//  2. AXME_MESH_DASHBOARD_URL env var
//  3. context-aware default (refuse non-prod gateway)
//  4. defaultMeshDashboardURL (prod)

func TestResolveDashboardURL_FlagOverride(t *testing.T) {
	t.Setenv("AXME_MESH_DASHBOARD_URL", "http://env-url.test")
	got, err := resolveDashboardURL("https://flag-override.test", "https://api.cloud.axme.ai")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "https://flag-override.test" {
		t.Errorf("expected flag override to win, got %s", got)
	}
}

func TestResolveDashboardURL_FlagDefaultIgnored(t *testing.T) {
	// If the user passes the default value, it should be treated as "no override"
	// so env var/context-aware logic kicks in.
	t.Setenv("AXME_MESH_DASHBOARD_URL", "http://env-url.test")
	got, err := resolveDashboardURL(defaultMeshDashboardURL, "https://api.cloud.axme.ai")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "http://env-url.test" {
		t.Errorf("expected env var to win when flag is default, got %s", got)
	}
}

func TestResolveDashboardURL_EnvVar(t *testing.T) {
	t.Setenv("AXME_MESH_DASHBOARD_URL", "http://env-url.test")
	got, err := resolveDashboardURL("", "https://api.cloud.axme.ai")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != "http://env-url.test" {
		t.Errorf("expected env var, got %s", got)
	}
}

func TestResolveDashboardURL_ProdDefault(t *testing.T) {
	os.Unsetenv("AXME_MESH_DASHBOARD_URL")
	got, err := resolveDashboardURL("", "https://api.cloud.axme.ai")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != defaultMeshDashboardURL {
		t.Errorf("expected %s, got %s", defaultMeshDashboardURL, got)
	}
}

func TestResolveDashboardURL_StagingFailsFast(t *testing.T) {
	os.Unsetenv("AXME_MESH_DASHBOARD_URL")
	_, err := resolveDashboardURL("", "https://axme-gateway-staging-1047255398033.us-central1.run.app")
	if err == nil {
		t.Fatal("expected error on non-prod gateway")
	}
	if !strings.Contains(err.Error(), "non-prod") {
		t.Errorf("expected 'non-prod' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "AXME_MESH_DASHBOARD_URL") {
		t.Errorf("expected env var hint in error, got: %v", err)
	}
}

func TestResolveDashboardURL_EmptyGatewayBaseURL(t *testing.T) {
	// Empty base URL (e.g. fresh config) should fall through to default
	os.Unsetenv("AXME_MESH_DASHBOARD_URL")
	got, err := resolveDashboardURL("", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != defaultMeshDashboardURL {
		t.Errorf("expected %s, got %s", defaultMeshDashboardURL, got)
	}
}
