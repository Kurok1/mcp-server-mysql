/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 2.0.1
 */
package profile

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Kurok1/mcp-server-mysql/internal/config"
)

type testCloser struct {
	closed bool
	err    error
}

func (c *testCloser) Close() error {
	c.closed = true
	return c.err
}

func profileConfig(database string) config.ProfileConfig {
	return config.ProfileConfig{MySQL: config.MySQLConfig{User: "user", Database: database}}
}

func TestNewManagerBuildsIndependentLazyRuntimes(t *testing.T) {
	disabledDir := filepath.Join(t.TempDir(), "not-created")
	manager, err := NewManager(map[string]config.ProfileConfig{
		"writer": {
			Description: "Write database",
			MySQL:       config.MySQLConfig{Host: "unreachable.invalid", User: "writer", Database: "write_db"},
			Security:    config.SecurityConfig{AllowedStatements: []string{"select", "insert"}, MaxRows: 42},
			Audit:       config.AuditConfig{LogDir: disabledDir},
		},
		"reader": profileConfig("read_db"),
	})
	if err != nil {
		t.Fatalf("NewManager must not connect eagerly: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	reader, err := manager.Get("reader")
	if err != nil {
		t.Fatal(err)
	}
	writer, err := manager.Get("writer")
	if err != nil {
		t.Fatal(err)
	}
	if reader.Executor == writer.Executor || reader.Guard == writer.Guard || reader.Audit == writer.Audit {
		t.Error("profiles must not share runtime dependencies")
	}
	if reader.Database != "read_db" || reader.MaxRows != 1000 || reader.MaxScriptStatements != 50 {
		t.Errorf("reader defaults = %+v", reader)
	}
	if writer.Database != "write_db" || writer.MaxRows != 42 {
		t.Errorf("writer settings = %+v", writer)
	}
	if _, err := os.Stat(disabledDir); !os.IsNotExist(err) {
		t.Errorf("disabled audit should not create log directory: %v", err)
	}
}

func TestManagerGetAndListHaveNoFallbackOrMutableMetadata(t *testing.T) {
	manager, err := NewManager(map[string]config.ProfileConfig{
		"zeta":  {Description: "Z", MySQL: config.MySQLConfig{User: "u", Database: "z"}},
		"alpha": {Description: "A", MySQL: config.MySQLConfig{User: "u", Database: "a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if _, err := manager.Get(""); err == nil {
		t.Error("empty profile name should not fall back")
	}
	if _, err := manager.Get("missing"); err == nil {
		t.Error("unknown profile should not fall back")
	}
	infos := manager.List()
	if len(infos) != 2 || infos[0] != (Info{Name: "alpha", Description: "A"}) || infos[1] != (Info{Name: "zeta", Description: "Z"}) {
		t.Errorf("List = %+v", infos)
	}
	infos[0].Name = "changed"
	if got := manager.List()[0].Name; got != "alpha" {
		t.Errorf("List leaked mutable backing slice: %q", got)
	}
}

func TestManagerSupportsConcurrentReadsAndIdempotentClose(t *testing.T) {
	manager, err := NewManager(map[string]config.ProfileConfig{"app": profileConfig("db")})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 100 {
				runtime, getErr := manager.Get("app")
				if getErr != nil || runtime.Name != "app" {
					t.Errorf("Get = %v, %v", runtime, getErr)
					return
				}
				if len(manager.List()) != 1 {
					t.Error("List returned unexpected profile count")
					return
				}
			}
		}()
	}
	wait.Wait()
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("second Close = %v", err)
	}
}

func TestNewManagerCleansUpAfterLocalInitializationFailure(t *testing.T) {
	alphaExecutor := &testCloser{}
	alphaAudit := &testCloser{err: errors.New("alpha audit close failed")}
	partialExecutor := &testCloser{}
	_, err := newManagerWithFactory(map[string]config.ProfileConfig{
		"alpha":  profileConfig("alpha_db"),
		"broken": profileConfig("broken_db"),
	}, func(name string, cfg config.ProfileConfig) (runtimeBuild, error) {
		switch name {
		case "alpha":
			return runtimeBuild{
				runtime: &Runtime{Name: name},
				resources: []managedResource{
					{profile: name, kind: "executor", closer: alphaExecutor},
					{profile: name, kind: "audit logger", closer: alphaAudit},
				},
			}, nil
		case "broken":
			// This models an executor created before its audit logger fails.
			return runtimeBuild{resources: []managedResource{
				{profile: name, kind: "executor", closer: partialExecutor},
			}}, errors.New("create audit logger")
		default:
			t.Fatalf("unexpected profile %q", name)
			return runtimeBuild{}, nil
		}
	})
	if err == nil {
		t.Fatal("newManagerWithFactory succeeded, want an error")
	}
	if !alphaExecutor.closed || !alphaAudit.closed || !partialExecutor.closed {
		t.Errorf("constructor failure did not close all resources: alpha executor=%t audit=%t partial executor=%t", alphaExecutor.closed, alphaAudit.closed, partialExecutor.closed)
	}
	if !errors.Is(err, alphaAudit.err) {
		t.Errorf("constructor error omitted cleanup failure: %v", err)
	}
}

func TestNewManagerRejectsEmptyAndInvalidProfiles(t *testing.T) {
	for name, profiles := range map[string]map[string]config.ProfileConfig{
		"empty":           nil,
		"invalid-name":    {"bad/name": profileConfig("db")},
		"invalid-profile": {"app": {MySQL: config.MySQLConfig{User: "u"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewManager(profiles); err == nil {
				t.Error("NewManager succeeded, want an error")
			}
		})
	}
}
