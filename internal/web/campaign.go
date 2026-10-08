package web

// Campaign setup (Lane I1, Phase 3): campaign.yaml model + GM setup wizard
// backing store.
//
// campaign.yaml keys are append-only after Phase 1 (roadmap). The frozen
// scaffold (E1 `init --bare`) writes only name/created; H1 owns the full
// pack/overlay/optionals vocabulary. Until H1 lands its vault packs under
// rules/ (fixtures/rules is still empty), this file validates SHAPE only
// (known keys, sane values) and records base/overlay as display-only strings
// (p05: "versions recorded, never enforced").
//
// No YAML dependency is available (frozen dep list, spec §2), so parsing and
// surgery cover exactly the frozen key set below — never a general YAML
// round-trip (which would drop comments/order and violate AGENTS.md).

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Campaign is the validated campaign.yaml model. Keys mirror the H1-frozen
// set: base, overlay, enabled-features[] (p05). Unknown keys are rejected by
// validation (append-only extension happens via explicit amend, never here).
type Campaign struct {
	Name            string
	Created         string
	Base            string
	Overlay         string
	EnabledFeatures []string
}

var featureIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ParseCampaign parses campaign.yaml content covering exactly the frozen key
// set. Comment lines (#...) and blank lines are skipped; enabled-features
// accepts a block list (`key:` followed by `- item` lines) or a flow list
// (`key: [a, b]`). Unknown top-level keys are an error (append-only keys
// change only via amend). Missing keys stay zero.
func ParseCampaign(content string) (*Campaign, error) {
	c := &Campaign{}
	lines := splitKeepEnds(content)
	inFeatures := false
	for _, raw := range lines {
		line := trimEOL(raw)
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if indent > 0 || (inFeatures && (strings.HasPrefix(trimmed, "- ") || trimmed == "-")) {
			if !inFeatures {
				continue // nested content under unknown parents: ignore
			}
			if !strings.HasPrefix(trimmed, "- ") && trimmed != "-" {
				continue
			}
			item := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			item = strings.Trim(item, `"'`)
			if item != "" {
				c.EnabledFeatures = append(c.EnabledFeatures, item)
			}
			continue
		}
		inFeatures = false
		j := strings.IndexByte(trimmed, ':')
		if j < 0 {
			continue // not a mapping line: ignore (never crash on vault content)
		}
		key := strings.TrimSpace(trimmed[:j])
		val := strings.TrimSpace(trimmed[j+1:])
		// Strip a trailing comment outside quotes.
		val = stripYAMLComment(val)
		switch key {
		case "name":
			c.Name = unquote(val)
		case "created":
			c.Created = unquote(val)
		case "base":
			c.Base = unquote(val)
		case "overlay":
			c.Overlay = unquote(val)
		case "enabled-features":
			if strings.HasPrefix(val, "[") {
				for _, item := range parseFlowList(val) {
					if item != "" {
						c.EnabledFeatures = append(c.EnabledFeatures, item)
					}
				}
			} else if val == "" {
				inFeatures = true
			} else {
				c.EnabledFeatures = append(c.EnabledFeatures, unquote(val))
			}
		default:
			return nil, fmt.Errorf("unknown campaign.yaml key: %q", key)
		}
	}
	return c, nil
}

// ValidateCampaign checks shape only (H1 owns pack existence): name is
// required, overlay requires base (p05 layers), feature ids are stable slugs.
func ValidateCampaign(c *Campaign) error {
	if c == nil {
		return fmt.Errorf("nil campaign")
	}
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("campaign name is required")
	}
	if c.Overlay != "" && c.Base == "" {
		return fmt.Errorf("overlay requires a base ruleset")
	}
	seen := map[string]bool{}
	for _, f := range c.EnabledFeatures {
		if !featureIDRe.MatchString(f) {
			return fmt.Errorf("bad feature id %q: want [a-z0-9-]", f)
		}
		if seen[f] {
			return fmt.Errorf("duplicate feature id %q", f)
		}
		seen[f] = true
	}
	return nil
}

// UpsertCampaignYAML writes c's keys back into existing content surgically:
// known `key: ...` lines are replaced in place (comments/blank lines/order
// preserved), missing keys are appended, and the enabled-features block is
// rebuilt as a `- item` list. Unknown keys already present are preserved
// verbatim (forward-compat with H1 additions) but new writes never invent
// keys outside the frozen set.
func UpsertCampaignYAML(existing string, c *Campaign) string {
	features := append([]string(nil), c.EnabledFeatures...)
	sort.Strings(features)
	vals := map[string]string{
		"name":    quoteYAML(c.Name),
		"created": quoteYAML(c.Created),
		"base":    quoteYAML(c.Base),
		"overlay": quoteYAML(c.Overlay),
	}
	// Drop empty optionals so `init --bare` scaffolds stay minimal.
	skip := map[string]bool{}
	for k, v := range vals {
		if v == `""` || v == "" {
			skip[k] = true
		}
	}
	lines := splitKeepEnds(existing)
	var out []string
	done := map[string]bool{}
	inFeatures := false
	featuresEmitted := false
	flushFeatures := func() {
		if featuresEmitted {
			return
		}
		featuresEmitted = true
		if len(features) == 0 {
			return
		}
		out = append(out, "enabled-features:\n")
		for _, f := range features {
			out = append(out, "- "+f+"\n")
		}
	}
	hasFence := false
	for i, raw := range lines {
		line := trimEOL(raw)
		eol := fmLineEnd(raw)
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if i == 0 && trimmed == "---" {
			hasFence = true
		}
		if indent > 0 {
			if inFeatures {
				continue // old feature items: dropped, rebuilt by flush
			}
			out = append(out, raw)
			continue
		}
		if inFeatures && (strings.HasPrefix(trimmed, "- ") || trimmed == "-") {
			continue // block-seq items at column 0: still feature items
		}
		if strings.HasPrefix(trimmed, "#") || trimmed == "" || trimmed == "---" || trimmed == "..." {
			if inFeatures {
				inFeatures = false
				flushFeatures()
			}
			out = append(out, raw)
			continue
		}
		j := strings.IndexByte(trimmed, ':')
		if j < 0 {
			out = append(out, raw)
			continue
		}
		key := strings.TrimSpace(trimmed[:j])
		if key == "enabled-features" {
			inFeatures = true
			flushFeatures()
			continue
		}
		if v, ok := vals[key]; ok {
			inFeatures = false
			if skip[key] {
				done[key] = true // cleared in the form: drop the line
				continue
			}
			out = append(out, key+": "+v+eol)
			done[key] = true
			continue
		}
		inFeatures = false
		out = append(out, raw)
	}
	if inFeatures {
		flushFeatures()
	}
	var missing []string
	for k, v := range vals {
		if !skip[k] && !done[k] {
			missing = append(missing, k+": "+v+"\n")
		}
	}
	sort.Strings(missing)
	out = append(out, missing...)
	if !featuresEmitted {
		flushFeatures()
	}
	_ = hasFence
	return strings.Join(out, "")
}

func stripYAMLComment(v string) string {
	inSingle, inDouble := false, false
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble && i > 0 && (v[i-1] == ' ' || v[i-1] == '\t') {
				return strings.TrimSpace(v[:i])
			}
		}
	}
	return v
}

func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
		q := v[0]
		inner := v[1 : len(v)-1]
		if q == '"' {
			inner = strings.ReplaceAll(inner, `\"`, `"`)
			inner = strings.ReplaceAll(inner, `\\`, `\`)
		} else {
			inner = strings.ReplaceAll(inner, `''`, `'`)
		}
		return inner
	}
	return v
}

func parseFlowList(v string) []string {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "[") {
		return nil
	}
	end := strings.LastIndex(v, "]")
	if end < 0 {
		return nil
	}
	var out []string
	for _, item := range strings.Split(v[1:end], ",") {
		item = unquote(stripYAMLComment(item))
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}
