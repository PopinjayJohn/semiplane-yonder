package plugins

import (
	"context"

	"github.com/semiplane/yonder/internal/web"
)

// Plugin defines the interface for plugins.
// Plugins can register slots, routes, and provide CSS/JS.
type Plugin interface {
	// ID returns the unique plugin identifier.
	ID() string
	// Name returns the human-readable plugin name.
	Name() string
	// Version returns the plugin version.
	Version() string
	// Init initializes the plugin with the registry.
	Init(ctx context.Context, reg *Registry) error
	// Routes returns additional routes for this plugin.
	Routes() []web.Route
	// Slots returns slot components for this plugin.
	Slots() []web.SlotComponent
	// CSS returns the plugin CSS (max 20KB, @layer plugins, [data-plugin] prefixed).
	CSS() string
	// JS returns the plugin JavaScript (CodeMirror 6 only, no Node in binary).
	JS() string
	// OnEnable is called when the plugin is enabled.
	OnEnable(ctx context.Context) error
	// OnDisable is called when the plugin is disabled.
	OnDisable(ctx context.Context) error
	// ConfigSchema returns the JSON schema for plugin configuration.
	ConfigSchema() string
	// ValidateConfig validates the plugin configuration.
	ValidateConfig(config map[string]any) error
}

// Registry manages plugin registration and lifecycle.
// Layering: web owns the frozen Route/SlotComponent types and registries;
// plugins is a consumer that registers into them (never the reverse — web
// never imports plugins, so no cycle). Lane I2 owns this package.
type Registry struct {
	plugins  map[string]Plugin
	enabled  map[string]bool
	slotReg  *web.SlotRegistry
	routeReg *web.RouteRegistry
}

// NewRegistry creates a new plugin registry.
func NewRegistry(slotReg *web.SlotRegistry, routeReg *web.RouteRegistry) *Registry {
	return &Registry{
		plugins:  make(map[string]Plugin),
		enabled:  make(map[string]bool),
		slotReg:  slotReg,
		routeReg: routeReg,
	}
}

// Register registers a plugin.
// Phase 0 stub: no logic (Lane I2 implements in Phase 3).
func (r *Registry) Register(p Plugin) error {
	return nil // not implemented
}

// Enable enables a plugin.
// Phase 0 stub: no logic (Lane I2 implements in Phase 3).
func (r *Registry) Enable(ctx context.Context, id string) error {
	return nil // not implemented
}

// Disable disables a plugin.
// Phase 0 stub: no logic (Lane I2 implements in Phase 3).
func (r *Registry) Disable(ctx context.Context, id string) error {
	return nil // not implemented
}

// Get returns a plugin by ID.
// Phase 0 stub.
func (r *Registry) Get(id string) (Plugin, bool) {
	return nil, false // not implemented
}

// List returns all registered plugins.
// Phase 0 stub.
func (r *Registry) List() []Plugin {
	return nil // not implemented
}

// Enabled returns all enabled plugins.
// Phase 0 stub.
func (r *Registry) Enabled() []Plugin {
	return nil // not implemented
}

// CSS returns concatenated CSS for all enabled plugins.
// Phase 0 stub.
func (r *Registry) CSS() string {
	return "" // not implemented
}

// JS returns concatenated JS for all enabled plugins.
// Phase 0 stub.
func (r *Registry) JS() string {
	return "" // not implemented
}

// ErrPluginExists is returned when registering a duplicate plugin.
var ErrPluginExists = &PluginError{Code: "EXISTS"}

// ErrPluginNotFound is returned when a plugin is not found.
var ErrPluginNotFound = &PluginError{Code: "NOT_FOUND"}

// PluginError represents a plugin error.
type PluginError struct {
	Code    string
	Message string
}

func (e *PluginError) Error() string {
	if e.Message != "" {
		return e.Code + ": " + e.Message
	}
	return e.Code
}

// FeatureFlag represents an optional feature that can be enabled in campaign.yaml.
// Features are registered by plugins and enabled via campaign.yaml enabled-features.
type FeatureFlag struct {
	ID           string
	Name         string
	Description  string
	PluginID     string
	Requires     []string
	Conflicts    []string
	Default      bool
	Experimental bool
}

// FeatureRegistry manages feature flags.
type FeatureRegistry struct {
	features map[string]FeatureFlag
	enabled  map[string]bool
}

// NewFeatureRegistry creates a new feature registry.
func NewFeatureRegistry() *FeatureRegistry {
	return &FeatureRegistry{
		features: make(map[string]FeatureFlag),
		enabled:  make(map[string]bool),
	}
}

// Register registers a feature flag.
// Phase 0 stub (Lane I2 implements in Phase 3).
func (r *FeatureRegistry) Register(f FeatureFlag) {
	// not implemented
}

// Enable enables a feature.
// Phase 0 stub (Lane I2 implements in Phase 3).
func (r *FeatureRegistry) Enable(id string) error {
	return nil // not implemented
}

// Disable disables a feature.
// Phase 0 stub (Lane I2 implements in Phase 3).
func (r *FeatureRegistry) Disable(id string) {
	// not implemented
}

// IsEnabled returns true if a feature is enabled.
// Phase 0 stub (Lane I2 implements in Phase 3).
func (r *FeatureRegistry) IsEnabled(id string) bool {
	return false // not implemented
}

// List returns all feature flags.
// Phase 0 stub (Lane I2 implements in Phase 3).
func (r *FeatureRegistry) List() []FeatureFlag {
	return nil // not implemented
}

var (
	ErrFeatureNotFound          = &PluginError{Code: "FEATURE_NOT_FOUND"}
	ErrFeatureConflict          = &PluginError{Code: "FEATURE_CONFLICT"}
	ErrFeatureMissingDependency = &PluginError{Code: "FEATURE_MISSING_DEPENDENCY"}
)
