package markdown

import (
	"context"
	"strings"
	"testing"
)

// Task checkboxes must carry an accessible name (axe "label" rule): the
// item text, or a state-prefixed fallback for empty items.
func TestTaskCheckBoxHasAccessibleName(t *testing.T) {
	ctx := context.Background()
	page, err := Parse(ctx, "- [ ] walk the plains\n- [x] read the gazetteer\n- [ ]\n", "tasks.md")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	html := page.HTML
	for _, want := range []string{
		`aria-label="walk the plains"`,
		`aria-label="read the gazetteer"`,
		`aria-label="incomplete task"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered HTML missing %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, `<input disabled="" type="checkbox"> `) ||
		strings.Contains(html, `<input checked="" disabled="" type="checkbox"> `) {
		t.Errorf("bare unlabeled checkbox rendered:\n%s", html)
	}
}
