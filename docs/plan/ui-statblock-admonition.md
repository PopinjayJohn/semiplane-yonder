# Plan — Statblock + Admonition UI

Decision: statblock first, callout/admonition polish second.

## 1. Research findings

### fantasy-statblocks (Obsidian-TTRPG-Community)

- Single syntax: `statblock` fenced code block with a YAML body.
- All fields optional. Core D&D shape:

```yaml
name, size, type, alignment, ac, hp, hit_dice, speed,
stats: [STR, DEX, CON, INT, WIS, CHA],
saves: [{ability: mod}], skillsaves: [{skill: mod}],
damage_vulnerabilities/resistances/immunities, condition_immunities,
senses, languages, cr,
traits/actions/bonus_actions/reactions/legendary_actions: [{name, desc}],
spells: [strings], image: [[vault-image]]
monster: <name>   # bestiary lookup + field override
layout: <name>    # custom layout selector
```

- Visual identity: parchment box (`#fdf1dc`), red header/rules
  (`#7a200d` / `#922610`), amber bar (`#e69a28`), ability-score table,
  ruled sections.
- CSS hooks: `.statblock`, `.<layout-slug>`, `.<monster-slug>`.

### admonition (ebullient/obsidian-admonition)

- Same 12 types as Obsidian built-in callouts: `note abstract info tip
  success question warning failure danger bug example quote`, plus aliases
  (`tldr summary todo hint important check done help faq caution attention
  fail missing error cite`).
- Two syntaxes, shared type names:
  - Callout: `> [!tip] title` (native Obsidian).
  - Code block: `ad-<type>` fence with header params `title:`,
    `collapse:` (`open`/`closed`/`none`), `icon:`, `color:` (RGB triad),
    `metadata:` (→ `data-callout-metadata`), then a markdown body.
- `title:` renders as markdown; blank title means title row omitted.

### Current state

- `> [!type]` parses to `CalloutNode`
  (`internal/markdown/obsidian.go:121`) and renders as
  `<div class="callout callout-<type>" data-callout data-fold>`
  (`internal/markdown/obsidian.go:525`). No title element, no icons,
  and zero `.callout` rules exist in the repo.
- `ad-*` and `statblock` fences render as plain `<pre><code>` and index
  as generic `code` chunks (`internal/markdown/parse.go:242`).
- Secrets are scrubbed server-side by div removal with depth counting
  (`internal/secrets/secrets.go:179`). New nodes must nest inside this
  mechanism, not beside it.

## 2. Scope (v1)

Statblock:

- Inline fields only. `monster:` renders an "extends/overrides X" note;
  no bestiary lookup or cross-note resolution.
- `layout:` accepted, echoed as `data-layout` slug; rendering stays the
  default Basic 5e shape.
- `image: [[...]]` resolves through the ACL-checked asset handler only.
- Dice strings (`2d10+8`) stay plain text; no roller integration.
- Wikilinks in `desc` reuse the existing wikilink path so secret-target
  redaction still applies.
- Malformed YAML renders a labeled fallback box with raw text; never
  crashes, never rewrites vault source (quarantine pattern).

Admonition (after statblock):

- Built-in callout polish: title row, `-/+` fold behaviour, per-type
  icon (inline SVG, no new dependency) plus color tokens.
- `ad-*` code blocks render through the same component: parse the five
  header params, render the markdown body inside.

## 3. Work items

### S1 — statblock parser + renderer (`internal/markdown/`)

- Add a `StatblockNode` block node.
- Intercept fenced code with info string `statblock` in the AST
  transformer (same pattern as `isUnsupportedFence`,
  `internal/markdown/obsidian.go:331`), replacing the fence node.
  `Page.Content` stays byte-identical.
- Parse the YAML body with the existing frontmatter parser subset:
  scalars, `stats: [6]`, `- name:`/`desc:` lists. Unknown keys ignored.
- Extractor (`internal/markdown/parse.go:181`): emit one `statblock`
  block; the secret flag follows enclosing secret-callout depth.
  Inner text feeds FTS chunks via the existing `ParsePage` path
  (`internal/web/parse_adapter.go:44`), which already passes through
  non-`unsupported` blocks.

### S2 — statblock box UI (vanilla CSS)

- Semantic markup: `<section class="statblock" aria-label>` plus a real
  `<table>` for the six abilities.
- Palette as tokens defaulting to the plugin's four variables; add
  dark-theme and `print.css` overrides.
- Constraints: `@layer core`, `[data-theme]` tokens, never color-only,
  visible focus, `prefers-reduced-motion` respected, read-only output.

### S3 — callout / admonition follow-up

- Callout title row and fold control (`<details>/<summary>` or
  `aria-expanded` button).
- `ad-*` fence support with `title/collapse/icon/color/metadata`
  handling, rendered as the same callout component.

## 4. Tests

- Goldens under `testdata/markdown/`: `statblock-basic`,
  `statblock-edge` (S1/S2), `ad-codeblock` (S3).
- Leak-matrix cases: statblock inside `> [!secret]-`; secret link target
  inside a statblock `desc`; unauthorized viewer gets placeholder plus
  redacted link, never raw text.
- Gates: `go build ./...`, affected package tests, `make check`.
- Standing rules: frozen dependency list (spec section 2), server-side
  secret filtering on every read path, atomic vault writes only,
  append-only frontmatter keys.
