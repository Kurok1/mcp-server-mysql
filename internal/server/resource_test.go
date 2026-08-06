/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 1.2.1
 */
package server

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Kurok1/mcp-server-mysql/internal/audit"
	"github.com/Kurok1/mcp-server-mysql/internal/config"
	"github.com/Kurok1/mcp-server-mysql/internal/executor"
	"github.com/Kurok1/mcp-server-mysql/internal/guard"
)

func TestNormalizeCreateTableDDL(t *testing.T) {
	ddl := "CREATE TABLE `t1` (\n" +
		"  `id` int NOT NULL AUTO_INCREMENT,\n" +
		"  `note` varchar(50) COMMENT 'column AUTO_INCREMENT=777',\n" +
		"  PRIMARY KEY (`id`)\n" +
		") ENGINE=InnoDB AUTO_INCREMENT=44 DEFAULT CHARSET=utf8mb4 COMMENT='table AUTO_INCREMENT=999'"

	got := normalizeCreateTableDDL(ddl)
	if strings.Contains(got, "ENGINE=InnoDB AUTO_INCREMENT=44") {
		t.Fatalf("table-level AUTO_INCREMENT was not removed:\n%s", got)
	}
	for _, want := range []string{
		"`id` int NOT NULL AUTO_INCREMENT",
		"column AUTO_INCREMENT=777",
		"table AUTO_INCREMENT=999",
		"ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("normalized DDL missing %q:\n%s", want, got)
		}
	}
}

func TestNormalizeCreateTableDDLIgnoresVersionComment(t *testing.T) {
	ddl := "CREATE TABLE `t1` (\n  `id` int NOT NULL\n) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 /*!99999 AUTO_INCREMENT=123 */"
	if got := normalizeCreateTableDDL(ddl); got != ddl {
		t.Fatalf("version comment was altered:\ngot:  %s\nwant: %s", got, ddl)
	}
}

func TestTableResourceUnavailable(t *testing.T) {
	for _, code := range []uint16{1044, 1049, 1109, 1142, 1146} {
		err := fmt.Errorf("wrapped: %w", &mysql.MySQLError{Number: code})
		if !tableResourceUnavailable(err) {
			t.Errorf("MySQL error %d should map to resource unavailable", code)
		}
	}
	if tableResourceUnavailable(&mysql.MySQLError{Number: 2006}) {
		t.Error("infrastructure error must not map to resource unavailable")
	}
}

func TestTableResourceURI(t *testing.T) {
	got := tableResourceURI(executor.TableRef{Database: "db name", Table: "events/2026"})
	if want := "mysql:///schema/db%20name/events%2F2026"; got != want {
		t.Fatalf("tableResourceURI = %q, want %q", got, want)
	}
}

func TestE2EResources(t *testing.T) {
	stack := startStack(t)
	ctx := context.Background()

	init := stack.sess.InitializeResult()
	if init == nil || init.Capabilities == nil || init.Capabilities.Resources == nil {
		t.Fatalf("initialize result does not advertise resources: %#v", init)
	}
	if init.Capabilities.Logging == nil {
		t.Error("explicit resource capabilities must preserve logging capability")
	}
	if !init.Capabilities.Resources.ListChanged {
		t.Error("resources capability must advertise listChanged")
	}

	listed, err := stack.sess.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(listed.Resources) != 1 {
		t.Fatalf("resources = %#v, want exactly myapp.t1", listed.Resources)
	}
	r := listed.Resources[0]
	if r.URI != "mysql:///schema/myapp/t1" || r.Name != "myapp.t1" || r.Title != "myapp.t1" {
		t.Errorf("unexpected resource identity: %#v", r)
	}
	if r.MIMEType != "application/sql" {
		t.Errorf("MIMEType = %q, want application/sql", r.MIMEType)
	}
	if strings.Contains(r.Description, "AUTO_INCREMENT=999") {
		t.Errorf("resource description leaked table comment: %q", r.Description)
	}

	readDDL := func() string {
		t.Helper()
		res, err := stack.sess.ReadResource(ctx, &mcp.ReadResourceParams{URI: r.URI})
		if err != nil {
			t.Fatalf("ReadResource: %v", err)
		}
		if len(res.Contents) != 1 {
			t.Fatalf("contents = %#v", res.Contents)
		}
		content := res.Contents[0]
		if content.URI != r.URI || content.MIMEType != "application/sql" {
			t.Errorf("unexpected content metadata: %#v", content)
		}
		return content.Text
	}

	ddl := readDDL()
	if !strings.Contains(ddl, "CREATE TABLE `t1`") || !strings.Contains(ddl, "`id` int NOT NULL AUTO_INCREMENT") {
		t.Errorf("unexpected DDL:\n%s", ddl)
	}
	if regexp.MustCompile(`ENGINE=[^ ]+ AUTO_INCREMENT=\d+`).MatchString(ddl) {
		t.Errorf("table-level AUTO_INCREMENT was not normalized:\n%s", ddl)
	}
	if !strings.Contains(ddl, "table AUTO_INCREMENT=999") {
		t.Errorf("table comment was altered:\n%s", ddl)
	}

	if _, err := stack.ex.Execute(ctx, "ALTER TABLE t1 ADD COLUMN email VARCHAR(100)"); err != nil {
		t.Fatalf("alter table: %v", err)
	}
	if ddl = readDDL(); !strings.Contains(ddl, "`email` varchar(100)") {
		t.Errorf("resource did not reflect ALTER TABLE:\n%s", ddl)
	}

	if _, err := stack.ex.Execute(ctx, "CREATE TABLE t2 (id INT PRIMARY KEY)"); err != nil {
		t.Fatalf("create t2: %v", err)
	}
	listed, err = stack.sess.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("ListResources after CREATE: %v", err)
	}
	if len(listed.Resources) != 1 {
		t.Errorf("resource snapshot changed after CREATE: %#v", listed.Resources)
	}

	statsText, isErr := callText(t, stack.sess, "mysql_stats", map[string]any{})
	if isErr || !strings.Contains(statsText, `"total": 0`) {
		t.Errorf("resource operations polluted mysql_stats: isErr=%v stats=%s", isErr, statsText)
	}

	if _, err := stack.ex.Execute(ctx, "DROP TABLE t1"); err != nil {
		t.Fatalf("drop t1: %v", err)
	}
	_, err = stack.sess.ReadResource(ctx, &mcp.ReadResourceParams{URI: r.URI})
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != mcp.CodeResourceNotFound {
		t.Fatalf("ReadResource after DROP error = %v, want code %d", err, mcp.CodeResourceNotFound)
	}
}

func TestE2EResourceIgnoresSelectStatementSetting(t *testing.T) {
	stack := startStack(t)
	if _, err := stack.ex.Execute(context.Background(), "CREATE TABLE t2 (id INT PRIMARY KEY)"); err != nil {
		t.Fatalf("create t2: %v", err)
	}
	cfg := *stack.cfg
	cfg.Security = stack.cfg.Security
	cfg.Security.AllowedStatements = []string{"insert"}
	cfg.Security.MaxRows = 1

	srv := Build(&cfg, guard.New(cfg.Security, cfg.MySQL.Database), stack.ex, stack.logger)
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	go func() { _ = srv.Run(ctx, st) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-resource-no-select", Version: "0"}, nil)
	sess, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })

	listed, err := sess.ListResources(ctx, nil)
	if err != nil || len(listed.Resources) != 2 {
		t.Fatalf("ListResources without select: resources=%#v err=%v", listed, err)
	}
	if _, err := sess.ReadResource(ctx, &mcp.ReadResourceParams{URI: listed.Resources[0].URI}); err != nil {
		t.Fatalf("ReadResource without select: %v", err)
	}
	exactTables, truncated, err := (&deps{
		g:  guard.New(cfg.Security, cfg.MySQL.Database),
		ex: stack.ex,
	}).listVisibleBaseTablesUpTo(ctx, 2)
	if err != nil || len(exactTables) != 2 || truncated {
		t.Fatalf("table list at exact limit: tables=%#v truncated=%v err=%v", exactTables, truncated, err)
	}

	tablesText, isErr := callText(t, sess, "mysql_list_tables", map[string]any{})
	if isErr {
		t.Fatalf("mysql_list_tables with max_rows=1 failed: %s", tablesText)
	}
	if !strings.Contains(tablesText, "myapp.t1") || strings.Contains(tablesText, "myapp.t2") {
		t.Fatalf("mysql_list_tables did not apply max_rows after whitelist filtering: %s", tablesText)
	}
	if !strings.Contains(tablesText, "truncated at 1 table") {
		t.Fatalf("mysql_list_tables did not report truncation: %s", tablesText)
	}
}

func TestResourceDiscoveryFailureKeepsServerAvailable(t *testing.T) {
	cfg := &config.Config{
		MySQL: config.MySQLConfig{
			Host: "127.0.0.1", Port: 1, User: "unreachable", Database: "myapp",
			Pool: config.PoolConfig{MaxOpen: 1, MaxIdle: 1},
		},
		Security: config.SecurityConfig{
			AllowedStatements: []string{"select"},
			TableWhitelist:    []string{"myapp.*"},
			MaxRows:           1000,
			QueryTimeout:      config.Duration(100 * time.Millisecond),
		},
		Audit: config.AuditConfig{
			LogDir:             t.TempDir(),
			SlowQueryThreshold: config.Duration(time.Second),
			RingBufferSize:     10,
		},
	}
	ex, err := executor.New(cfg.MySQL, cfg.Security)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ex.Close() })
	logger, err := audit.NewLogger(cfg.Audit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(logger.Close)

	srv := Build(cfg, guard.New(cfg.Security, cfg.MySQL.Database), ex, logger)
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	go func() { _ = srv.Run(ctx, st) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-resource-unreachable", Version: "0"}, nil)
	sess, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })

	listed, err := sess.ListResources(ctx, nil)
	if err != nil {
		t.Fatalf("ListResources after discovery failure: %v", err)
	}
	if len(listed.Resources) != 0 {
		t.Fatalf("resources = %#v, want empty list", listed.Resources)
	}
	tools, err := sess.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 7 {
		t.Fatalf("server tools unavailable after discovery failure: tools=%#v err=%v", tools, err)
	}
}
