package plugins

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/web"
)

// Plugin defines the interface for plugins.
// Plugins can register slots, routes, and provide CSS/JS.
//
// Red line (P06): UI plugins receive secret-filtered input only. Every
// SlotComponent a plugin contributes must set SecretFiltered=true; the
// registry and `plugin check` reject anything else.
type Plugin interface {
	// ID returns the unique plugin identifier (lowercase alnum + dashes).
	ID() string
	// Name returns the human-readable plugin name.
	Name() string
	// Version returns the plugin version.
	Version() string
	// Init initializes the plugin with the registry.
	Init(ctx context.Context, reg *Registry) error
	// Routes returns additional routes for this plugin.
	// Registered via the frozen web.RouteRegistry (never by editing core
	// routing); each handler is wrapped so a disabled plugin 404s.
	Routes() []web.Route
	// Slots returns slot components for this plugin.
	// All entries must set SecretFiltered=true.
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
	// A11y returns the accessibility metadata `plugin check` lints
	// (landmarks + labels; P06 fails the check when these are missing).
	A11y() A11ySpec
}

// A11ySpec declares a plugin's keyboard/screen-reader contract for linting.
type A11ySpec struct {
	// Landmarks lists the ARIA landmarks every fragment renders
	// (e.g. "complementary", "main").
	Landmarks []string
	// Labels lists the accessible names of interactive controls.
	Labels []string
	// LiveRegions lists aria-live regions ("polite" and/or "assertive").
	LiveRegions []string
}

// Registry manages plugin registration and lifecycle.
// Layering: web owns the frozen Route/SlotComponent types and registries;
// plugins is a consumer that registers into them (never the reverse — web
// never imports plugins, so no cycle). Lane I2 owns this package.
type Registry struct {
	mu         sync.RWMutex
	plugins    map[string]Plugin
	enabled    map[string]bool
	gates      map[string]*pluginGate
	routesDone map[string]bool
	slotReg    *web.SlotRegistry
	routeReg   *web.RouteRegistry
}

// pluginGate flips route handlers to a uniform 404 when the plugin is
// disabled. The web.RouteRegistry is append-only, so gating (not removal)
// is how a disabled plugin leaves no UI trace on its routes; SlotsFor
// (below) does the same for its slots.
type pluginGate struct {
	mu      sync.RWMutex
	enabled bool
}

func (g *pluginGate) allow() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.enabled
}

func (g *pluginGate) set(on bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.enabled = on
}

// NewRegistry creates a new plugin registry.
func NewRegistry(slotReg *web.SlotRegistry, routeReg *web.RouteRegistry) *Registry {
	return &Registry{
		plugins:    make(map[string]Plugin),
		enabled:    make(map[string]bool),
		gates:      make(map[string]*pluginGate),
		routesDone: make(map[string]bool),
		slotReg:    slotReg,
		routeReg:   routeReg,
	}
}

// Register registers a plugin. IDs must be valid (see ValidPluginID) and
// unique; slot contributions must be secret-filtered (red line).
func (r *Registry) Register(p Plugin) error {
	if p == nil {
		return &PluginError{Code: "INVALID", Message: "nil plugin"}
	}
	if !ValidPluginID(p.ID()) {
		return &PluginError{Code: "INVALID", Message: fmt.Sprintf("bad plugin id %q", p.ID())}
	}
	for _, s := range p.Slots() {
		if err := ValidateSlot(p.ID(), s); err != nil {
			return err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.plugins[p.ID()]; dup {
		return &PluginError{Code: "EXISTS", Message: p.ID()}
	}
	r.plugins[p.ID()] = p
	r.gates[p.ID()] = &pluginGate{}
	return nil
}

// Enable enables a plugin: runs OnEnable, registers its routes once
// (handlers gated so a later Disable 404s), and registers its slots.
func (r *Registry) Enable(ctx context.Context, id string) error {
	r.mu.Lock()
	p, ok := r.plugins[id]
	if !ok {
		r.mu.Unlock()
		return &PluginError{Code: "NOT_FOUND", Message: id}
	}
	gate := r.gates[id]
	slotReg, routeReg := r.slotReg, r.routeReg
	firstRoutes := !r.routesDone[id]
	if firstRoutes {
		r.routesDone[id] = true
	}
	r.mu.Unlock()

	if err := p.OnEnable(ctx); err != nil {
		return err
	}
	if firstRoutes && routeReg != nil {
		for _, rt := range p.Routes() {
			routeReg.Register(gatedRoute(id, gate, rt))
		}
	}
	if slotReg != nil {
		for _, s := range p.Slots() {
			slotReg.Register(s.SlotName, s)
		}
	}
	gate.set(true)
	r.mu.Lock()
	r.enabled[id] = true
	r.mu.Unlock()
	return nil
}

// Disable disables a plugin: routes flip to 404 via their gates and SlotsFor
// / CSS / JS stop exposing it, so no UI trace remains. Slots registered into
// the append-only web.SlotRegistry stay registered but are filtered out by
// SlotsFor; callers must render slots via SlotsFor, not the raw registry.
func (r *Registry) Disable(ctx context.Context, id string) error {
	r.mu.Lock()
	p, ok := r.plugins[id]
	if !ok {
		r.mu.Unlock()
		return &PluginError{Code: "NOT_FOUND", Message: id}
	}
	gate := r.gates[id]
	r.mu.Unlock()

	if err := p.OnDisable(ctx); err != nil {
		return err
	}
	gate.set(false)
	r.mu.Lock()
	delete(r.enabled, id)
	r.mu.Unlock()
	return nil
}

// gatedRoute wraps a plugin route handler so requests 404 (uniform, no leak)
// while the plugin is disabled.
func gatedRoute(id string, gate *pluginGate, rt web.Route) web.Route {
	inner := rt.Handler
	rt.Handler = func(w http.ResponseWriter, req *http.Request) {
		if !gate.allow() {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if inner != nil {
			inner(w, req)
		}
	}
	return rt
}

// Get returns a plugin by ID.
func (r *Registry) Get(id string) (Plugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.plugins[id]
	return p, ok
}

// List returns all registered plugins, sorted by ID.
func (r *Registry) List() []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Plugin, 0, len(r.plugins))
	for _, p := range r.plugins {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// Enabled returns all enabled plugins, sorted by ID.
func (r *Registry) Enabled() []Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Plugin
	for id := range r.enabled {
		if p, ok := r.plugins[id]; ok {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}

// IsEnabled reports whether a plugin is currently enabled.
func (r *Registry) IsEnabled(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.enabled[id]
}

// SlotsFor returns the slot components for enabled plugins only, delegating
// filtering/priority order to the web registry. Shells must render via this,
// never the raw registry, or disabled plugins leak UI traces.
func (r *Registry) SlotsFor(slot string, viewer *auth.Viewer, path string) []web.SlotComponent {
	if r.slotReg == nil {
		return nil
	}
	all := r.slotReg.Get(slot, viewer, path)
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []web.SlotComponent
	for _, c := range all {
		if c.PluginID != "" && !r.enabled[c.PluginID] {
			continue
		}
		out = append(out, c)
	}
	return out
}

// CSS returns concatenated CSS for all enabled plugins.
func (r *Registry) CSS() string {
	var b strings.Builder
	for _, p := range r.Enabled() {
		if css := p.CSS(); css != "" {
			b.WriteString(css)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// JS returns concatenated JS for all enabled plugins.
func (r *Registry) JS() string {
	var b strings.Builder
	for _, p := range r.Enabled() {
		if js := p.JS(); js != "" {
			b.WriteString(js)
			b.WriteString("\n")
		}
	}
	return b.String()
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
// Features are registered by plugins and enabled via campaign.yaml
// enabled-plugins — NEVER enabled-features (phase decision G3: ruleset
// optionals own enabled-features; plugin features own enabled-plugins;
// separate registries, separate config keys).
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
	mu       sync.RWMutex
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

// Register registers a feature flag (idempotent on ID; last write wins).
func (r *FeatureRegistry) Register(f FeatureFlag) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.features[f.ID] = f
	if f.Default {
		r.enabled[f.ID] = true
	}
}

// Enable enables a feature, checking conflicts and dependencies.
func (r *FeatureRegistry) Enable(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.features[id]
	if !ok {
		return &PluginError{Code: "FEATURE_NOT_FOUND", Message: id}
	}
	for _, c := range f.Conflicts {
		if r.enabled[c] {
			return &PluginError{Code: "FEATURE_CONFLICT", Message: id + " conflicts with " + c}
		}
	}
	for _, dep := range f.Requires {
		if !r.enabled[dep] {
			return &PluginError{Code: "FEATURE_MISSING_DEPENDENCY", Message: id + " requires " + dep}
		}
	}
	r.enabled[id] = true
	return nil
}

// Disable disables a feature. Dependents are left enabled (fail open would be
// wrong for rules, but features are UI-only; dependents degrade). Callers
// that need cascade should check IsEnabled per render.
func (r *FeatureRegistry) Disable(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.enabled, id)
}

// IsEnabled returns true if a feature is enabled.
func (r *FeatureRegistry) IsEnabled(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.enabled[id]
}

// List returns all feature flags, sorted by ID.
func (r *FeatureRegistry) List() []FeatureFlag {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]FeatureFlag, 0, len(r.features))
	for _, f := range r.features {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

var (
	ErrFeatureNotFound          = &PluginError{Code: "FEATURE_NOT_FOUND"}
	ErrFeatureConflict          = &PluginError{Code: "FEATURE_CONFLICT"}
	ErrFeatureMissingDependency = &PluginError{Code: "FEATURE_MISSING_DEPENDENCY"}
)
