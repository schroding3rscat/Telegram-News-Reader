package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndValidate(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	data := `
listen: 127.0.0.1:8080
data_dir: data
admin:
  username: admin
  password_bcrypt: "$2a$12$012345678901234567890u12345678901234567890123456789012"
  query_token: "123456789012345678901234"
llm:
  ad_threshold: 0.8
  review_threshold: 0.5
pipeline:
  workers: 1
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(cfg.DataDir, dir) {
		t.Fatalf("relative data dir was not resolved: %s", cfg.DataDir)
	}
	if cfg.Pipeline.Retention.Hours() != 168 {
		t.Fatalf("unexpected retention: %s", cfg.Pipeline.Retention)
	}
}

func TestRejectsPublicListen(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.Listen = "0.0.0.0:8080"
	cfg.Admin.Username = "admin"
	cfg.Admin.Password = "$2a$12$hash"
	cfg.Admin.QueryToken = strings.Repeat("x", 24)
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected public listen address to be rejected")
	}
}
