/**
 * @author Kurok1 <im.kurokyhanc@gmail.com>
 * @since 2.0.1
 */
// Package profile builds and owns the independent runtime dependencies for
// each configured MySQL profile.
package profile

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/Kurok1/mcp-server-mysql/internal/audit"
	"github.com/Kurok1/mcp-server-mysql/internal/config"
	"github.com/Kurok1/mcp-server-mysql/internal/executor"
	"github.com/Kurok1/mcp-server-mysql/internal/guard"
)

// Info is the safe, public description of an available profile.
type Info struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Runtime contains the resources used by a single profile. Each Runtime has
// independent executor, guard, and audit instances.
type Runtime struct {
	Name                string
	Description         string
	Executor            *executor.Executor
	Guard               *guard.Guard
	Audit               *audit.Logger
	Database            string
	MaxRows             int
	MaxScriptStatements int
}

// Manager owns all profile runtimes. Its map is fully constructed before it is
// published and is never mutated, so Get does not hold a lock while callers
// execute SQL through the returned Runtime.
type Manager struct {
	runtimes  map[string]*Runtime
	infos     []Info
	resources []managedResource

	closeOnce sync.Once
	closeErr  error
}

type resourceCloser interface {
	Close() error
}

type managedResource struct {
	profile string
	kind    string
	closer  resourceCloser
}

type runtimeBuild struct {
	runtime   *Runtime
	resources []managedResource
}

type runtimeFactory func(string, config.ProfileConfig) (runtimeBuild, error)

// NewManager validates and normalizes direct Go configuration, then creates
// the lazy executor and independent guard/audit dependencies for every profile.
func NewManager(profiles map[string]config.ProfileConfig) (*Manager, error) {
	return newManagerWithFactory(profiles, newRuntime)
}

func newManagerWithFactory(profiles map[string]config.ProfileConfig, factory runtimeFactory) (*Manager, error) {
	if len(profiles) == 0 {
		return nil, fmt.Errorf("profiles must contain at least one profile")
	}

	names := make([]string, 0, len(profiles))
	for name := range profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	m := &Manager{runtimes: make(map[string]*Runtime, len(profiles)), infos: make([]Info, 0, len(profiles))}
	for _, name := range names {
		if err := config.ValidateProfileName(name); err != nil {
			return nil, initializationError(name, err, m.closeCreated())
		}
		profileConfig := profiles[name]
		if err := config.NormalizeAndValidateProfile(&profileConfig); err != nil {
			return nil, initializationError(name, err, m.closeCreated())
		}

		built, err := factory(name, profileConfig)
		m.resources = append(m.resources, built.resources...)
		if err != nil {
			return nil, initializationError(name, err, m.closeCreated())
		}
		m.runtimes[name] = built.runtime
		m.infos = append(m.infos, Info{Name: name, Description: profileConfig.Description})
	}
	return m, nil
}

func initializationError(profileName string, initErr, cleanupErr error) error {
	initErr = fmt.Errorf("initialize profile %q: %w", profileName, initErr)
	if cleanupErr == nil {
		return initErr
	}
	return errors.Join(initErr, cleanupErr)
}

func newRuntime(name string, cfg config.ProfileConfig) (runtimeBuild, error) {
	ex, err := executor.New(cfg.MySQL, cfg.Security)
	if err != nil {
		return runtimeBuild{}, fmt.Errorf("create executor: %w", err)
	}
	executorResource := managedResource{profile: name, kind: "executor", closer: ex}
	logger, err := audit.NewLogger(name, cfg.Audit)
	if err != nil {
		return runtimeBuild{resources: []managedResource{executorResource}}, fmt.Errorf("create audit logger: %w", err)
	}
	return runtimeBuild{runtime: &Runtime{
		Name:                name,
		Description:         cfg.Description,
		Executor:            ex,
		Guard:               guard.New(cfg.Security, cfg.MySQL.Database),
		Audit:               logger,
		Database:            cfg.MySQL.Database,
		MaxRows:             cfg.Security.MaxRows,
		MaxScriptStatements: cfg.Security.MaxScriptStatements,
	}, resources: []managedResource{
		executorResource,
		{profile: name, kind: "audit logger", closer: logger},
	}}, nil
}

// Get returns exactly the named profile. There is intentionally no implicit
// default or fallback profile.
func (m *Manager) Get(name string) (*Runtime, error) {
	runtime, ok := m.runtimes[name]
	if !ok {
		return nil, fmt.Errorf("unknown profile %q", name)
	}
	return runtime, nil
}

// List returns a fresh, name-sorted slice containing only safe profile metadata.
func (m *Manager) List() []Info {
	infos := make([]Info, len(m.infos))
	copy(infos, m.infos)
	return infos
}

// Close is idempotent and closes every executor and audit logger, reporting all
// close failures that occur.
func (m *Manager) Close() error {
	m.closeOnce.Do(func() {
		m.closeErr = m.closeCreated()
	})
	return m.closeErr
}

func (m *Manager) closeCreated() error {
	var errs []error
	for _, resource := range m.resources {
		if err := resource.closer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close %s for profile %q: %w", resource.kind, resource.profile, err))
		}
	}
	return errors.Join(errs...)
}
