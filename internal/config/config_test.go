/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const validYAML = `
resources:
  enabled: false
profiles:
  analytics:
    description: Analytics replica
    mysql:
      user: reader
      password: ${TEST_MYSQL_PW}
      database: analytics
    security:
      allowed_statements: [select]
      max_rows: 500
      query_timeout: 10s
    audit:
      log_dir: /tmp/audit
  writer:
    mysql:
      host: db.internal
      port: 3307
      user: writer
      password: password
      database: app
    security:
      allowed_statements: [select, insert]
      max_script_statements: 7
    audit:
      enabled: true
      ring_buffer_size: 12
`

func TestLoadNormalizesEachProfileIndependently(t *testing.T) {
	t.Setenv("TEST_MYSQL_PW", "s3cret")
	cfg, err := Load(writeTemp(t, validYAML))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	analytics := cfg.Profiles["analytics"]
	writer := cfg.Profiles["writer"]
	if analytics.MySQL.Password != "s3cret" {
		t.Errorf("env expansion failed: %q", analytics.MySQL.Password)
	}
	if analytics.MySQL.Host != "127.0.0.1" || analytics.MySQL.Port != 3306 {
		t.Errorf("analytics host defaults = %s:%d", analytics.MySQL.Host, analytics.MySQL.Port)
	}
	if analytics.MySQL.Pool.MaxOpen != 5 || analytics.MySQL.Pool.MaxIdle != 2 {
		t.Errorf("analytics pool defaults = %+v", analytics.MySQL.Pool)
	}
	if analytics.Security.MaxRows != 500 || time.Duration(analytics.Security.QueryTimeout) != 10*time.Second {
		t.Errorf("analytics security = %+v", analytics.Security)
	}
	if writer.MySQL.Host != "db.internal" || writer.MySQL.Port != 3307 {
		t.Errorf("writer mysql = %+v", writer.MySQL)
	}
	if writer.Description != "" {
		t.Errorf("omitted description = %q, want empty", writer.Description)
	}
	if writer.Security.MaxRows != 1000 || time.Duration(writer.Security.QueryTimeout) != 30*time.Second {
		t.Errorf("writer defaults = %+v", writer.Security)
	}
	if analytics.Audit.RingBufferSize != 1000 || writer.Audit.RingBufferSize != 12 {
		t.Errorf("audit defaults should be independent: analytics=%d writer=%d", analytics.Audit.RingBufferSize, writer.Audit.RingBufferSize)
	}
	if cfg.Resources.IsEnabled() {
		t.Error("resources.enabled false was not retained")
	}
}

func TestLoadExpandsTildePerProfile(t *testing.T) {
	cfg, err := Load(writeTemp(t, `
profiles:
  app:
    mysql: {user: u, password: p, database: d}
    audit: {log_dir: ~/x}
`))
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if got := cfg.Profiles["app"].Audit.LogDir; got != filepath.Join(home, "x") {
		t.Errorf("log_dir = %q", got)
	}
}

func TestLoadRejectsMalformedProfiles(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"empty map", "profiles: {}\n", "at least one"},
		{"missing profiles", "resources: {enabled: true}\n", "at least one"},
		{"invalid name", "profiles: {'Bad Name': {mysql: {user: u, password: p, database: d}}}\n", "invalid profile name"},
		{"legacy root mysql", "mysql: {user: u, password: p, database: d}\nprofiles: {app: {mysql: {user: u, password: p, database: d}}}\n", "field mysql not found"},
		{"legacy root security", "security: {}\nprofiles: {app: {mysql: {user: u, password: p, database: d}}}\n", "field security not found"},
		{"legacy root audit", "audit: {}\nprofiles: {app: {mysql: {user: u, password: p, database: d}}}\n", "field audit not found"},
		{"nested resources", "profiles: {app: {mysql: {user: u, password: p, database: d}, resources: {enabled: true}}}\n", "field resources not found"},
		{"unknown root", "profiles: {app: {mysql: {user: u, password: p, database: d}}}\nunknown: true\n", "field unknown not found"},
		{"unknown nested", "profiles: {app: {mysql: {user: u, password: p, database: d, nope: true}}}\n", "field nope not found"},
		{"duplicate profile", "profiles:\n  app: {mysql: {user: u, password: p, database: d}}\n  app: {mysql: {user: v, password: p, database: d}}\n", "mapping key"},
		{"missing mysql user", "profiles: {app: {mysql: {password: p, database: d}}}\n", "profile \"app\": mysql.user"},
		{"bad statement", "profiles: {app: {mysql: {user: u, password: p, database: d}, security: {allowed_statements: [drop]}}}\n", "allowed_statements"},
		{"negative audit ring", "profiles: {app: {mysql: {user: u, password: p, database: d}, audit: {ring_buffer_size: -1}}}\n", "ring_buffer_size"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, tc.yaml))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Load error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestProfileErrorsDoNotExposeCredentials(t *testing.T) {
	_, err := Load(writeTemp(t, `
profiles:
  private:
    mysql: {user: u, password: should-not-appear, database: ""}
`))
	if err == nil {
		t.Fatal("Load succeeded, want an error")
	}
	if !strings.Contains(err.Error(), `profile "private"`) {
		t.Errorf("error lacks profile context: %v", err)
	}
	if strings.Contains(err.Error(), "should-not-appear") {
		t.Errorf("error leaked credential: %v", err)
	}
}

func TestNormalizeAndValidateDirectConfig(t *testing.T) {
	cfg := &Config{Profiles: map[string]ProfileConfig{
		"app": {MySQL: MySQLConfig{User: "u", Database: "d"}},
	}}
	if err := cfg.NormalizeAndValidate(); err != nil {
		t.Fatal(err)
	}
	p := cfg.Profiles["app"]
	if p.MySQL.Host != "127.0.0.1" || p.MySQL.Port != 3306 || p.Security.MaxRows != 1000 {
		t.Errorf("defaults = %+v", p)
	}
	if len(p.Security.AllowedStatements) != 1 || p.Security.AllowedStatements[0] != "select" {
		t.Errorf("allowed statements = %v", p.Security.AllowedStatements)
	}
	if p.Security.BlockUnfilteredWrites != nil {
		t.Error("block_unfiltered_writes must remain nil so the guard uses its secure default")
	}
	if !cfg.Resources.IsEnabled() {
		t.Error("zero value resources must be enabled")
	}
}

func TestResourcesEnabledRejectsInvalidValues(t *testing.T) {
	for _, yaml := range []string{
		"profiles: {app: {mysql: {user: u, password: p, database: d}}}\nresources: {enabled: sometimes}\n",
		"profiles: {app: {mysql: {user: u, password: p, database: d}}}\nresources: {unknown: true}\n",
	} {
		if _, err := Load(writeTemp(t, yaml)); err == nil {
			t.Errorf("Load(%q) succeeded, want an error", yaml)
		}
	}
}
