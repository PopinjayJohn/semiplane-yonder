package campaign

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Frozen campaign.yaml key set (P04/P05, roadmap: append-only after Phase
// 1). Ruleset optionals (enabled-features) and feature plugins
// (enabled-plugins) are separate namespaces; the validator knows both so
// neither lane's future keys trip the other.
var campaignKeys = []string{
	"name",
	"created",
	"base",
	"base-version",
	"overlay",
	"overlay-version",
	"enabled-features",
	"enabled-plugins",
	"landing-page",
}

// ErrNoCampaign reports a missing campaign.yaml (I1's wizard creates it).
var ErrNoCampaign = errors.New("campaign.yaml not found")

// Campaign is the validated vault campaign descriptor. Versions are
// display-only in v1: recorded here, never enforced by the resolver.
type Campaign struct {
	Name            string
	Created         string
	Base            string
	BaseVersion     string
	Overlay         string
	OverlayVersion  string
	EnabledFeatures []string
	EnabledPlugins  []string
	LandingPage     string
}

// Load reads and validates <vault>/campaign.yaml. Legacy scaffold files
// (name/created only, written by `init --bare` before H1) validate clean
// with empty ruleset fields — I1's setup wizard fills in the rest.
func Load(vaultRoot string) (*Campaign, error) {
	path := filepath.Join(vaultRoot, "campaign.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoCampaign
		}
		return nil, fmt.Errorf("read campaign.yaml: %w", err)
	}
	return Parse(data)
}

// Parse validates campaign.yaml bytes. Unknown keys fail (closed set above);
// bad types fail with line numbers; everything else passes through.
func Parse(data []byte) (*Campaign, error) {
	doc, err := parseDoc(string(data))
	if err != nil {
		return nil, fmt.Errorf("campaign.yaml: %v", err)
	}
	allowed := map[string]bool{}
	for _, k := range campaignKeys {
		allowed[k] = true
	}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !allowed[k] {
			return nil, fmt.Errorf("campaign.yaml line %d: unknown key %q", doc[k].Line, k)
		}
	}
	c := &Campaign{}
	str := func(key string, dst *string) error {
		n, ok := doc[key]
		if !ok || n.Value == nil {
			return nil
		}
		s, ok := n.String()
		if !ok {
			return fmt.Errorf("campaign.yaml line %d: %q wants a string", n.Line, key)
		}
		*dst = s
		return nil
	}
	for _, kv := range []struct {
		key string
		dst *string
	}{
		{"name", &c.Name},
		{"created", &c.Created},
		{"base", &c.Base},
		{"base-version", &c.BaseVersion},
		{"overlay", &c.Overlay},
		{"overlay-version", &c.OverlayVersion},
		{"landing-page", &c.LandingPage},
	} {
		if err := str(kv.key, kv.dst); err != nil {
			return nil, err
		}
	}
	list := func(key string) ([]string, error) {
		n, ok := doc[key]
		if !ok || n.Value == nil {
			return []string{}, nil
		}
		l, ok := n.StringList()
		if !ok {
			return nil, fmt.Errorf("campaign.yaml line %d: %q wants a list of strings", n.Line, key)
		}
		return l, nil
	}
	if c.EnabledFeatures, err = list("enabled-features"); err != nil {
		return nil, err
	}
	if c.EnabledPlugins, err = list("enabled-plugins"); err != nil {
		return nil, err
	}
	return c, nil
}
