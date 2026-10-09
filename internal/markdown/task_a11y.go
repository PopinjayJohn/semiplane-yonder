package markdown

import (
	"html"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

// accessibleTaskRenderer overrides goldmark's stock TaskList checkbox output.
// Stock output is a bare `<input disabled type=checkbox>` with no accessible
// name — an axe "label" violation on every page containing a task list.
// This renders the identical control with an aria-label derived from the
// task text (state-prefixed fallback when the item has no text).
//
// Precedence: the stock renderer registers at priority 500, so this one
// registers below it (499, ascending wins) and only handles
// extast.KindTaskCheckBox — every other node falls through to the stock and
// custom renderers untouched. No parser changes.
type accessibleTaskRenderer struct{}

func (r *accessibleTaskRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(extast.KindTaskCheckBox, r.renderTaskCheckBox)
}

func (r *accessibleTaskRenderer) renderTaskCheckBox(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	node, ok := n.(*extast.TaskCheckBox)
	if !ok {
		return ast.WalkContinue, nil
	}
	label := taskCheckBoxLabel(node, source)
	if node.IsChecked {
		_, _ = w.WriteString(`<input checked="" disabled="" type="checkbox"`)
	} else {
		_, _ = w.WriteString(`<input disabled="" type="checkbox"`)
	}
	_, _ = w.WriteString(` aria-label="` + html.EscapeString(label) + `"`)
	_, _ = w.WriteString(`> `)
	return ast.WalkContinue, nil
}

// taskCheckBoxLabel derives the checkbox's accessible name from the item's
// own inline text (following siblings within the same block), normalized to
// single spaces and capped for sanity. Falls back to a state-prefixed
// generic name when the item carries no text.
func taskCheckBoxLabel(node *extast.TaskCheckBox, source []byte) string {
	var b strings.Builder
	for sib := node.NextSibling(); sib != nil; sib = sib.NextSibling() {
		if sib.Type() == ast.TypeBlock {
			break
		}
		switch t := sib.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(source))
			b.WriteByte(' ')
		case *ast.String:
			b.Write(t.Value)
			b.WriteByte(' ')
		default:
			// Structured inlines: collect their text one level down.
			_ = ast.Walk(sib, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
				if !entering {
					return ast.WalkContinue, nil
				}
				if tx, ok := n.(*ast.Text); ok {
					b.Write(tx.Segment.Value(source))
					b.WriteByte(' ')
				}
				return ast.WalkContinue, nil
			})
		}
		if b.Len() > 120 {
			break
		}
	}
	s := strings.Join(strings.Fields(b.String()), " ")
	if s == "" {
		if node.IsChecked {
			return "completed task"
		}
		return "incomplete task"
	}
	if len(s) > 80 {
		for len(s) > 80 {
			_, size := utf8.DecodeLastRuneInString(s)
			s = s[:len(s)-size]
		}
		s = strings.TrimSpace(s) + "…"
	}
	return s
}
