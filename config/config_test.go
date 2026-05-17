package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	return p
}

func TestLoad_Valid(t *testing.T) {
	body := `
google_domain: example.com
zitadel_org_id: "372844504547344154"
managed_group: all-staff@example.com
projects:
  - id: "123456789"
    groups:
      - engineers@example.com
      - sre@example.com
  - id: "987654321"
    groups:
      - finance@example.com
`
	cfg, err := Load(writeTemp(t, "ok.yaml", body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GoogleDomain != "example.com" {
		t.Errorf("google_domain = %q", cfg.GoogleDomain)
	}
	if cfg.ManagedGroup != "all-staff@example.com" {
		t.Errorf("managed_group = %q", cfg.ManagedGroup)
	}
	if len(cfg.Projects) != 2 {
		t.Fatalf("projects len = %d", len(cfg.Projects))
	}
	if cfg.Projects[0].ID != "123456789" || len(cfg.Projects[0].Groups) != 2 {
		t.Errorf("projects[0] = %+v", cfg.Projects[0])
	}
}

func TestLoad_MissingDomain(t *testing.T) {
	body := `
projects:
  - id: "1"
    groups: [a@example.com]
`
	_, err := Load(writeTemp(t, "x.yaml", body))
	if err == nil || !strings.Contains(err.Error(), "google_domain") {
		t.Fatalf("expected google_domain error, got %v", err)
	}
}

func TestLoad_MissingOrgID(t *testing.T) {
	body := `
google_domain: example.com
managed_group: reynholm
projects:
  - id: "1"
    groups: [a@example.com]
`
	_, err := Load(writeTemp(t, "x.yaml", body))
	if err == nil || !strings.Contains(err.Error(), "zitadel_org_id") {
		t.Fatalf("expected zitadel_org_id error, got %v", err)
	}
}

func TestLoad_MissingManagedGroup(t *testing.T) {
	body := `
google_domain: example.com
zitadel_org_id: "123"
projects:
  - id: "1"
    groups: [a@example.com]
`
	_, err := Load(writeTemp(t, "x.yaml", body))
	if err == nil || !strings.Contains(err.Error(), "managed_group") {
		t.Fatalf("expected managed_group error, got %v", err)
	}
}

func TestLoad_NoProjects(t *testing.T) {
	body := `
google_domain: example.com
zitadel_org_id: "123"
managed_group: reynholm
`
	_, err := Load(writeTemp(t, "x.yaml", body))
	if err == nil || !strings.Contains(err.Error(), "projects") {
		t.Fatalf("expected projects error, got %v", err)
	}
}

func TestLoad_DuplicateProjectID(t *testing.T) {
	body := `
google_domain: example.com
zitadel_org_id: "123"
managed_group: reynholm
projects:
  - id: "1"
    groups: [a@example.com]
  - id: "1"
    groups: [b@example.com]
`
	_, err := Load(writeTemp(t, "x.yaml", body))
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestLoad_UnknownField(t *testing.T) {
	body := `
google_domain: example.com
bogus_field: oops
projects:
  - id: "1"
    groups: [a@example.com]
`
	_, err := Load(writeTemp(t, "x.yaml", body))
	if err == nil || !strings.Contains(err.Error(), "bogus_field") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestLoad_EmptyPath(t *testing.T) {
	if _, err := Load(""); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}
