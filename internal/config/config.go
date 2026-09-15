/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 0.1.0
 */
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration 包装 time.Duration 以支持 YAML 中的 "30s" 写法。
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(dur)
	return nil
}

type PoolConfig struct {
	MaxOpen int `yaml:"max_open"`
	MaxIdle int `yaml:"max_idle"`
}

type MySQLConfig struct {
	Host     string     `yaml:"host"`
	Port     int        `yaml:"port"`
	User     string     `yaml:"user"`
	Password string     `yaml:"password"`
	Database string     `yaml:"database"`
	Pool     PoolConfig `yaml:"pool"`
}

type SecurityConfig struct {
	AllowedStatements []string `yaml:"allowed_statements"`
	TableWhitelist    []string `yaml:"table_whitelist"`
	MaxRows           int      `yaml:"max_rows"`
	QueryTimeout      Duration `yaml:"query_timeout"`
	// nil 表示未配置，guard 侧按 true（拦截）处理——默认往严的方向落。
	BlockUnfilteredWrites *bool `yaml:"block_unfiltered_writes"`
	// 单个 mysql_script 脚本允许的语句条数上限。
	MaxScriptStatements int `yaml:"max_script_statements"`
}

type AuditConfig struct {
	// Enabled 为 false（默认）时不落盘 JSONL 审计日志，也不创建日志目录；
	// 会话内统计（mysql_stats 的环形缓冲）不受影响，始终工作。
	Enabled            bool     `yaml:"enabled"`
	LogDir             string   `yaml:"log_dir"`
	SlowQueryThreshold Duration `yaml:"slow_query_threshold"`
	RingBufferSize     int      `yaml:"ring_buffer_size"`
}

type ResourcesConfig struct {
	// Enabled is nil when omitted (including YAML null), which keeps resources enabled.
	Enabled *bool `yaml:"enabled"`
}

// IsEnabled reports whether MCP resources should be registered. Its zero value
// intentionally enables resources so Config values built directly in Go retain
// the same behavior as an omitted YAML setting.
func (c ResourcesConfig) IsEnabled() bool {
	return c.Enabled == nil || *c.Enabled
}

type Config struct {
	Profiles  map[string]ProfileConfig `yaml:"profiles"`
	Resources ResourcesConfig          `yaml:"resources"`
}

// ProfileConfig groups the independently configured connection, guard, and
// audit settings for one named database profile.
type ProfileConfig struct {
	Description string         `yaml:"description"`
	MySQL       MySQLConfig    `yaml:"mysql"`
	Security    SecurityConfig `yaml:"security"`
	Audit       AuditConfig    `yaml:"audit"`
}

var validStatements = map[string]bool{
	"select": true, "insert": true, "update": true, "delete": true, "ddl": true,
}

// 白名单模式：db 部分.table 部分，各自允许字母数字下划线 $ 和通配符 *。
var whitelistPattern = regexp.MustCompile(`^[\w$*]+\.[\w$*]+$`)
var profileNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	// ${ENV_VAR} 引用展开
	expanded := os.Expand(string(raw), os.Getenv)

	cfg := &Config{}
	dec := yaml.NewDecoder(strings.NewReader(expanded))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}
	if err := cfg.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// NormalizeAndValidate applies the safe defaults and validates every profile.
// It is exported so callers that construct Config values directly get the same
// guarantees as YAML-loaded configurations.
func (c *Config) NormalizeAndValidate() error {
	if len(c.Profiles) == 0 {
		return fmt.Errorf("profiles must contain at least one profile")
	}
	for name, profile := range c.Profiles {
		if err := ValidateProfileName(name); err != nil {
			return err
		}
		if err := NormalizeAndValidateProfile(&profile); err != nil {
			return fmt.Errorf("profile %q: %w", name, err)
		}
		c.Profiles[name] = profile
	}
	return nil
}

// ValidateProfileName keeps profile names safe for lookup, logging, and audit
// file names. It deliberately accepts only a small, portable character set.
func ValidateProfileName(name string) error {
	if !profileNamePattern.MatchString(name) {
		return fmt.Errorf("invalid profile name %q (expected ^[a-z0-9][a-z0-9_-]*$)", name)
	}
	return nil
}

// NormalizeAndValidateProfile applies defaults and validates one profile.
// Manager construction uses it too, so direct Go construction is safe.
func NormalizeAndValidateProfile(p *ProfileConfig) error {
	if p.MySQL.Host == "" {
		p.MySQL.Host = "127.0.0.1"
	}
	if p.MySQL.Port == 0 {
		p.MySQL.Port = 3306
	}
	if p.MySQL.Pool.MaxOpen == 0 {
		p.MySQL.Pool.MaxOpen = 5
	}
	if p.MySQL.Pool.MaxIdle == 0 {
		p.MySQL.Pool.MaxIdle = 2
	}
	if len(p.Security.AllowedStatements) == 0 {
		p.Security.AllowedStatements = []string{"select"}
	}
	if p.Security.MaxRows == 0 {
		p.Security.MaxRows = 1000
	}
	if p.Security.QueryTimeout == 0 {
		p.Security.QueryTimeout = Duration(30 * time.Second)
	}
	if p.Security.MaxScriptStatements == 0 {
		p.Security.MaxScriptStatements = 50
	}
	if p.Audit.LogDir == "" {
		home, _ := os.UserHomeDir()
		p.Audit.LogDir = filepath.Join(home, ".mcp-server-mysql", "logs")
	}
	if p.Audit.SlowQueryThreshold == 0 {
		p.Audit.SlowQueryThreshold = Duration(time.Second)
	}
	if p.Audit.RingBufferSize == 0 {
		p.Audit.RingBufferSize = 1000
	}
	if strings.HasPrefix(p.Audit.LogDir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p.Audit.LogDir = filepath.Join(home, p.Audit.LogDir[2:])
		}
	}
	return validateProfile(p)
}

func validateProfile(p *ProfileConfig) error {
	if p.MySQL.User == "" {
		return fmt.Errorf("mysql.user must not be empty")
	}
	if p.MySQL.Database == "" {
		return fmt.Errorf("mysql.database must not be empty (used to qualify unqualified table names)")
	}
	for _, s := range p.Security.AllowedStatements {
		if !validStatements[s] {
			return fmt.Errorf("allowed_statements contains unknown statement type %q (valid: select/insert/update/delete/ddl)", s)
		}
	}
	for _, pattern := range p.Security.TableWhitelist {
		if !whitelistPattern.MatchString(pattern) {
			return fmt.Errorf("invalid table_whitelist pattern %q (expected db.table form, * wildcard allowed)", pattern)
		}
	}
	if p.Security.MaxScriptStatements < 0 {
		return fmt.Errorf("max_script_statements must not be negative")
	}
	if p.Audit.RingBufferSize < 1 {
		return fmt.Errorf("audit.ring_buffer_size must be positive")
	}
	return nil
}
