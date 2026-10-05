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
func (r *Registry) Register(p Plugin) error {
	if _, exists := r.plugins[p.ID()]; exists {
		return ErrPluginExists
	}
	r.plugins[p.ID()] = p
	return nil
}

// Enable enables a plugin.
func (r *Registry) Enable(ctx context.Context, id string) error {
	p, ok := r.plugins[id]
	if !ok {
		return ErrPluginNotFound
	}
	if r.enabled[id] {
		return nil
	}
	if err := p.Init(ctx, r); err != nil {
		return err
	}
	// Register slots - plugin returns SlotComponent with SlotName field
	for _, slot := range p.Slots() {
		r.slotReg.Register(slot.SlotName, slot)
	}
	// Register routes
	for _, route := range p.Routes() {
		r.routeReg.Register(route)
	}
	r.enabled[id] = true
	return p.OnEnable(ctx)
}

// Disable disables a plugin.
func (r *Registry) Disable(ctx context.Context, id string) error {
	p, ok := r.plugins[id]
	if !ok {
		return ErrPluginNotFound
	}
	if !r.enabled[id] {
		return nil
	}
	r.enabled[id] = false
	return p.OnDisable(ctx)
}

// Get returns a plugin by ID.
func (r *Registry) Get(id string) (Plugin, bool) {
	p, ok := r.plugins[id]
	return p, ok
}

// List returns all registered plugins.
func (r *Registry) List() []Plugin {
	result := make([]Plugin, 0, len(r.plugins))
	for _, p := range r.plugins {
		result = append(result, p)
	}
	return result
}

// Enabled returns all enabled plugins.
func (r *Registry) Enabled() []Plugin {
	result := make([]Plugin, 0)
	for id, p := range r.plugins {
		if r.enabled[id] {
			result = append(result, p)
		}
	}
	return result
}

// CSS returns concatenated CSS for all enabled plugins.
func (r *Registry) CSS() string {
	var css string
	for id := range r.enabled {
		if r.enabled[id] {
			css += r.plugins[id].CSS() + "\n"
		}
	}
	return css
}

// JS returns concatenated JS for all enabled plugins.
func (r *Registry) JS() string {
	var js string
	for id := range r.enabled {
		if r.enabled[id] {
			js += r.plugins[id].JS() + "\n"
		}
	}
	return js
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
func (r *FeatureRegistry) Register(f FeatureFlag) {
	r.features[f.ID] = f
	if f.Default {
		r.enabled[f.ID] = true
	}
}

// Enable enables a feature.
func (r *FeatureRegistry) Enable(id string) error {
	f, ok := r.features[id]
	if !ok {
		return ErrFeatureNotFound
	}
	// Check conflicts
	for _, conflict := range f.Conflicts {
		if r.enabled[conflict] {
			return ErrFeatureConflict
		}
	}
	// Check requires
	for _, req := range f.Requires {
		if !r.enabled[req] {
			return ErrFeatureMissingDependency
		}
	}
	r.enabled[id] = true
	return nil
}

// Disable disables a feature.
func (r *FeatureRegistry) Disable(id string) {
	r.enabled[id] = false
}

// IsEnabled returns true if a feature is enabled.
func (r *FeatureRegistry) IsEnabled(id string) bool {
	return r.enabled[id]
}

// List returns all feature flags.
func (r *FeatureRegistry) List() []FeatureFlag {
	result := make([]FeatureFlag, 0, len(r.features))
	for _, f := range r.features {
		result = append(result, f)
	}
	return result
}

var (
	ErrFeatureNotFound          = &PluginError{Code: "FEATURE_NOT_FOUND"}
	ErrFeatureConflict          = &PluginError{Code: "FEATURE_CONFLICT"}
	ErrFeatureMissingDependency = &PluginError{Code: "FEATURE_MISSING_DEPENDENCY"}
)
