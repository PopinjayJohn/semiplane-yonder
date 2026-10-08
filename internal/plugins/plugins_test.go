package plugins

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/semiplane/yonder/internal/web"
)

type fakePlugin struct {
	id      string
	slots   []web.SlotComponent
	routes  []web.Route
	css     string
	js      string
	a11y    A11ySpec
	enabled bool
}

func (f *fakePlugin) ID() string                                { return f.id }
func (f *fakePlugin) Name() string                              { return "fake " + f.id }
func (f *fakePlugin) Version() string                           { return "0.1" }
func (f *fakePlugin) Init(_ context.Context, _ *Registry) error { return nil }
func (f *fakePlugin) Routes() []web.Route                       { return f.routes }
func (f *fakePlugin) Slots() []web.SlotComponent                { return f.slots }
func (f *fakePlugin) CSS() string                               { return f.css }
func (f *fakePlugin) JS() string                                { return f.js }
func (f *fakePlugin) OnEnable(_ context.Context) error {
	f.enabled = true
	return nil
}
func (f *fakePlugin) OnDisable(_ context.Context) error {
	f.enabled = false
	return nil
}
func (f *fakePlugin) ConfigSchema() string                  { return "{}" }
func (f *fakePlugin) ValidateConfig(_ map[string]any) error { return nil }
func (f *fakePlugin) A11y() A11ySpec                        { return f.a11y }

func testPlugin(id string) *fakePlugin {
	return &fakePlugin{
		id:   id,
		a11y: A11ySpec{Landmarks: []string{"complementary"}, Labels: []string{"roll"}},
		slots: []web.SlotComponent{{
			SlotName: web.SlotSidebarRight, ID: "w", Component: "w",
			SecretFiltered: true, PluginID: id,
		}},
	}
}

func TestRegisterDuplicate(t *testing.T) {
	r := NewRegistry(web.NewSlotRegistry(), web.NewRouteRegistry())
	if err := r.Register(testPlugin("alpha")); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(testPlugin("alpha")); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestRegisterBadID(t *testing.T) {
	r := NewRegistry(web.NewSlotRegistry(), web.NewRouteRegistry())
	if err := r.Register(testPlugin("Bad_ID!")); err == nil {
		t.Fatal("expected invalid id error")
	}
}

func TestRegisterUnfilteredSlotRejected(t *testing.T) {
	r := NewRegistry(web.NewSlotRegistry(), web.NewRouteRegistry())
	p := testPlugin("leaky")
	p.slots = []web.SlotComponent{{
		SlotName: web.SlotSidebarLeft, ID: "w", Component: "w", PluginID: "leaky",
		// SecretFiltered: false — red-line violation
	}}
	if err := r.Register(p); err == nil {
		t.Fatal("expected unfiltered slot rejection")
	}
}

func TestEnableDisableLifecycle(t *testing.T) {
	ctx := context.Background()
	slotReg := web.NewSlotRegistry()
	routeReg := web.NewRouteRegistry()
	r := NewRegistry(slotReg, routeReg)
	hit := false
	p := testPlugin("alpha")
	p.routes = []web.Route{{
		Method: http.MethodGet, Path: "/alpha",
		Handler: func(w http.ResponseWriter, _ *http.Request) { hit = true },
	}}
	if err := r.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := r.Enable(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if !r.IsEnabled("alpha") || len(r.Enabled()) != 1 {
		t.Fatal("expected alpha enabled")
	}
	// Route serves while enabled.
	for _, rt := range routeReg.Routes() {
		rec := httptest.NewRecorder()
		rt.Handler(rec, httptest.NewRequest(http.MethodGet, "/alpha", nil))
		if rec.Code != http.StatusOK || !hit {
			t.Fatalf("enabled route: code=%d hit=%v", rec.Code, hit)
		}
	}
	// Slots visible while enabled.
	if got := r.SlotsFor(web.SlotSidebarRight, nil, ""); len(got) != 1 {
		t.Fatalf("enabled slots = %d, want 1", len(got))
	}
	if got := r.CSS(); got != "" {
		t.Fatalf("unexpected CSS %q", got)
	}
	if err := r.Disable(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if r.IsEnabled("alpha") || len(r.Enabled()) != 0 {
		t.Fatal("expected alpha disabled")
	}
	// Route 404s while disabled (no UI trace).
	for _, rt := range routeReg.Routes() {
		rec := httptest.NewRecorder()
		rt.Handler(rec, httptest.NewRequest(http.MethodGet, "/alpha", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("disabled route code = %d, want 404", rec.Code)
		}
	}
	// Slots hidden while disabled.
	if got := r.SlotsFor(web.SlotSidebarRight, nil, ""); len(got) != 0 {
		t.Fatalf("disabled slots = %d, want 0", len(got))
	}
	// Re-enable works without double route registration.
	if err := r.Enable(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if n := len(routeReg.Routes()); n != 1 {
		t.Fatalf("routes = %d after re-enable, want 1", n)
	}
}

func TestEnableMissing(t *testing.T) {
	r := NewRegistry(web.NewSlotRegistry(), web.NewRouteRegistry())
	if err := r.Enable(context.Background(), "ghost"); err == nil {
		t.Fatal("expected not-found")
	}
	if err := r.Disable(context.Background(), "ghost"); err == nil {
		t.Fatal("expected not-found")
	}
}

func TestListSorted(t *testing.T) {
	r := NewRegistry(web.NewSlotRegistry(), web.NewRouteRegistry())
	for _, id := range []string{"zeta", "alpha", "mid"} {
		if err := r.Register(testPlugin(id)); err != nil {
			t.Fatal(err)
		}
	}
	got := r.List()
	if len(got) != 3 || got[0].ID() != "alpha" || got[2].ID() != "zeta" {
		t.Fatalf("unsorted list: %v", got)
	}
	if _, ok := r.Get("mid"); !ok {
		t.Fatal("Get(mid) missing")
	}
}

func TestFeatureRegistryConflictsDeps(t *testing.T) {
	fr := NewFeatureRegistry()
	fr.Register(FeatureFlag{ID: "base", Name: "base", Default: true})
	fr.Register(FeatureFlag{ID: "x", Name: "x", Requires: []string{"base"}})
	fr.Register(FeatureFlag{ID: "y", Name: "y", Conflicts: []string{"x"}})
	if !fr.IsEnabled("base") {
		t.Fatal("default feature should be enabled")
	}
	if err := fr.Enable("x"); err != nil {
		t.Fatal(err)
	}
	if err := fr.Enable("y"); err == nil {
		t.Fatal("expected conflict error")
	}
	fr2 := NewFeatureRegistry()
	fr2.Register(FeatureFlag{ID: "z", Name: "z", Requires: []string{"missing"}})
	if err := fr2.Enable("z"); err == nil {
		t.Fatal("expected missing-dependency error")
	}
	if err := fr2.Enable("ghost"); err == nil {
		t.Fatal("expected not-found error")
	}
	fr.Disable("x")
	if fr.IsEnabled("x") {
		t.Fatal("disable failed")
	}
	if len(fr.List()) != 3 {
		t.Fatalf("list = %d, want 3", len(fr.List()))
	}
}
