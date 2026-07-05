/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const validYAML = `
mysql:
  host: 127.0.0.1
  port: 3306
  user: mcp_dev
  password: ${TEST_MYSQL_PW}
  database: myapp
security:
  allowed_statements: [select, insert]
  table_whitelist:
    - "myapp.*"
    - "shop.orders"
  max_rows: 500
  query_timeout: 10s
audit:
  log_dir: /tmp/audit
  slow_query_threshold: 2s
`

func TestLoadValid(t *testing.T) {
	t.Setenv("TEST_MYSQL_PW", "s3cret")
	cfg, err := Load(writeTemp(t, validYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MySQL.Password != "s3cret" {
		t.Errorf("env expansion failed: %q", cfg.MySQL.Password)
	}
	if cfg.Security.MaxRows != 500 {
		t.Errorf("max_rows = %d", cfg.Security.MaxRows)
	}
	if time.Duration(cfg.Security.QueryTimeout) != 10*time.Second {
		t.Errorf("query_timeout = %v", cfg.Security.QueryTimeout)
	}
	// 未显式配置的项取默认值
	if cfg.MySQL.Pool.MaxOpen != 5 || cfg.MySQL.Pool.MaxIdle != 2 {
		t.Errorf("pool defaults = %+v", cfg.MySQL.Pool)
	}
	if cfg.Audit.RingBufferSize != 1000 {
		t.Errorf("ring_buffer_size = %d", cfg.Audit.RingBufferSize)
	}
	if cfg.Security.BlockUnfilteredWrites != nil {
		t.Error("block_unfiltered_writes unset should stay nil (guard treats nil as true)")
	}
}

func TestLoadDefaultsAreSafe(t *testing.T) {
	minimal := `
mysql:
  user: u
  password: p
  database: d
`
	cfg, err := Load(writeTemp(t, minimal))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Security.AllowedStatements) != 1 || cfg.Security.AllowedStatements[0] != "select" {
		t.Errorf("default allowed_statements = %v, want [select]", cfg.Security.AllowedStatements)
	}
	if len(cfg.Security.TableWhitelist) != 0 {
		t.Errorf("default whitelist should be empty (deny all), got %v", cfg.Security.TableWhitelist)
	}
	if cfg.MySQL.Host != "127.0.0.1" || cfg.MySQL.Port != 3306 {
		t.Errorf("host/port defaults = %s:%d", cfg.MySQL.Host, cfg.MySQL.Port)
	}
	if time.Duration(cfg.Security.QueryTimeout) != 30*time.Second {
		t.Errorf("default query_timeout = %v", cfg.Security.QueryTimeout)
	}
	if cfg.Security.MaxRows != 1000 {
		t.Errorf("default max_rows = %d", cfg.Security.MaxRows)
	}
}

func TestTildeExpansion(t *testing.T) {
	cfg, err := Load(writeTemp(t, `
mysql: {user: u, password: p, database: d}
audit:
  log_dir: ~/x
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	home, _ := os.UserHomeDir()
	if cfg.Audit.LogDir != filepath.Join(home, "x") {
		t.Errorf("log_dir = %q", cfg.Audit.LogDir)
	}
}

func TestLoadRejectsInvalid(t *testing.T) {
	cases := []struct{ name, yaml string }{
		{"unknown statement", `
mysql: {user: u, password: p, database: d}
security:
  allowed_statements: [select, drop]
`},
		{"bad whitelist pattern", `
mysql: {user: u, password: p, database: d}
security:
  table_whitelist: ["no-dot-pattern"]
`},
		{"missing user", `
mysql: {password: p, database: d}
`},
		{"missing database", `
mysql: {user: u, password: p}
`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Load(writeTemp(t, c.yaml)); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestMaxScriptStatementsDefault(t *testing.T) {
	cfg, err := Load(writeTemp(t, `
mysql: {user: u, password: p, database: d}
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Security.MaxScriptStatements != 50 {
		t.Errorf("default max_script_statements = %d, want 50", cfg.Security.MaxScriptStatements)
	}
}

func TestMaxScriptStatementsRejectNegative(t *testing.T) {
	_, err := Load(writeTemp(t, `
mysql: {user: u, password: p, database: d}
security:
  max_script_statements: -1
`))
	if err == nil {
		t.Error("expected error for negative max_script_statements")
	}
}

func TestAuditEnabledDefaultsFalse(t *testing.T) {
	cfg, err := Load(writeTemp(t, `
mysql: {user: u, password: p, database: d}
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Audit.Enabled {
		t.Error("audit.enabled should default to false (no disk logging)")
	}
}

func TestAuditEnabledSet(t *testing.T) {
	cfg, err := Load(writeTemp(t, `
mysql: {user: u, password: p, database: d}
audit:
  enabled: true
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Audit.Enabled {
		t.Error("audit.enabled: true should be parsed as true")
	}
}
