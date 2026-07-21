package config

import (
	"os"
	"testing"
)

func TestDevAuditAutoConfig(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("JWT_SECRET", "replace-this-with-at-least-32-random-characters")
	t.Setenv("APP_ADDRESS", ":8080")
	t.Setenv("AUDIT_INGEST_TOKEN", "")
	t.Setenv("PUBLIC_BASE_URL", "")
	// Clear so Load sees empty
	os.Unsetenv("AUDIT_INGEST_TOKEN")
	os.Unsetenv("PUBLIC_BASE_URL")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuditIngestToken == "" {
		t.Fatal("development 应自动派生 AUDIT_INGEST_TOKEN")
	}
	if cfg.PublicBaseURL != "http://127.0.0.1:8080" {
		t.Fatalf("PublicBaseURL = %q", cfg.PublicBaseURL)
	}
	if cfg.AuditIngestURL != "http://127.0.0.1:8080/audit-api/records" {
		t.Fatalf("AuditIngestURL = %q", cfg.AuditIngestURL)
	}
	t.Logf("token=%s url=%s", cfg.AuditIngestToken, cfg.AuditIngestURL)
}

func TestProdAuditRequiresExplicit(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("JWT_SECRET", "replace-this-with-at-least-32-random-characters")
	t.Setenv("APP_ADDRESS", ":8080")
	os.Unsetenv("AUDIT_INGEST_TOKEN")
	os.Unsetenv("PUBLIC_BASE_URL")
	os.Unsetenv("EMBEDDED_SFU")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuditIngestToken != "" {
		t.Fatalf("production 未配置时应为空，got %q", cfg.AuditIngestToken)
	}
	if cfg.AuditIngestURL != "" {
		t.Fatalf("production 未配置时 URL 应为空，got %q", cfg.AuditIngestURL)
	}
	if cfg.EmbeddedSFU {
		t.Fatal("production 默认不应开启内嵌 SFU")
	}
}

func TestDevEmbeddedSFUDefaultOn(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("JWT_SECRET", "replace-this-with-at-least-32-random-characters")
	t.Setenv("APP_ADDRESS", ":8080")
	os.Unsetenv("EMBEDDED_SFU")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EmbeddedSFU {
		t.Fatal("development 默认应开启内嵌 SFU")
	}
	if cfg.EmbeddedSFUAdvertiseWSS == "" {
		t.Fatal("应推导 advertise wss")
	}
}
