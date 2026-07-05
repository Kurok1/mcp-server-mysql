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
		return fmt.Errorf("非法时长 %q: %w", s, err)
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
	LogDir             string   `yaml:"log_dir"`
	SlowQueryThreshold Duration `yaml:"slow_query_threshold"`
	RingBufferSize     int      `yaml:"ring_buffer_size"`
}

type Config struct {
	MySQL    MySQLConfig    `yaml:"mysql"`
	Security SecurityConfig `yaml:"security"`
	Audit    AuditConfig    `yaml:"audit"`
}

var validStatements = map[string]bool{
	"select": true, "insert": true, "update": true, "delete": true, "ddl": true,
}

// 白名单模式：db 部分.table 部分，各自允许字母数字下划线 $ 和通配符 *。
var whitelistPattern = regexp.MustCompile(`^[\w$*]+\.[\w$*]+$`)

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件: %w", err)
	}
	// ${ENV_VAR} 引用展开
	expanded := os.Expand(string(raw), os.Getenv)

	cfg := &Config{}
	dec := yaml.NewDecoder(strings.NewReader(expanded))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.MySQL.Host == "" {
		c.MySQL.Host = "127.0.0.1"
	}
	if c.MySQL.Port == 0 {
		c.MySQL.Port = 3306
	}
	if c.MySQL.Pool.MaxOpen == 0 {
		c.MySQL.Pool.MaxOpen = 5
	}
	if c.MySQL.Pool.MaxIdle == 0 {
		c.MySQL.Pool.MaxIdle = 2
	}
	if len(c.Security.AllowedStatements) == 0 {
		c.Security.AllowedStatements = []string{"select"}
	}
	if c.Security.MaxRows == 0 {
		c.Security.MaxRows = 1000
	}
	if c.Security.QueryTimeout == 0 {
		c.Security.QueryTimeout = Duration(30 * time.Second)
	}
	if c.Security.MaxScriptStatements == 0 {
		c.Security.MaxScriptStatements = 50
	}
	if c.Audit.LogDir == "" {
		home, _ := os.UserHomeDir()
		c.Audit.LogDir = filepath.Join(home, ".mcp-server-mysql", "logs")
	}
	if c.Audit.SlowQueryThreshold == 0 {
		c.Audit.SlowQueryThreshold = Duration(time.Second)
	}
	if c.Audit.RingBufferSize == 0 {
		c.Audit.RingBufferSize = 1000
	}
	if strings.HasPrefix(c.Audit.LogDir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			c.Audit.LogDir = filepath.Join(home, c.Audit.LogDir[2:])
		}
	}
}

func (c *Config) validate() error {
	if c.MySQL.User == "" {
		return fmt.Errorf("mysql.user 不能为空")
	}
	if c.MySQL.Database == "" {
		return fmt.Errorf("mysql.database 不能为空（用于补全未带库名的表）")
	}
	for _, s := range c.Security.AllowedStatements {
		if !validStatements[s] {
			return fmt.Errorf("allowed_statements 含未知语句类型 %q（可选: select/insert/update/delete/ddl）", s)
		}
	}
	for _, p := range c.Security.TableWhitelist {
		if !whitelistPattern.MatchString(p) {
			return fmt.Errorf("table_whitelist 模式 %q 非法（要求 db.table 形式，可用通配符 *）", p)
		}
	}
	if c.Security.MaxScriptStatements < 0 {
		return fmt.Errorf("max_script_statements 不能为负数")
	}
	return nil
}
