# FuzzParse `unstable render` — root cause, fix, repro

## Symptom
`go test ./internal/markdown/ -fuzz=FuzzParse` intermittently failed at
`markdown_fuzz_test.go:50: unstable render`: re-parsing the SAME input
produced different HTML. CI run 38742436845 FAILED fuzz-short while run
38742427333 on the same commit PASSED (input-dependent flake; failing input
hash `032ee9596b224f18` was never checked in, so the exact CI input is gone).

## Root cause
Three `for k := range <map>` loops in `internal/markdown/statblock.go`
normalized keys while iterating a Go map (random order per iteration), so
colliding keys resolved to a random winner — and the winner feeds the
rendered HTML:

1. `parseStatblockBody` (`statblock.go:142`): normalized `name`/`Name`,
   `hit-dice`/`hit_dice`, `AC`/`ac` … to one key. Random winner → random
   name/AC/hit-dice in HTML.
2. `sbTraits` (`statblock.go:272`): matched `name`/`desc` case-insensitively
   while ranging the trait map. `name: A` + `NAME: B` in one item → random
   trait name in HTML.
3. `sbModsFromMap` (`statblock.go:354`): lowercased keys into a `lower` map
   while ranging. `MOD: 5` + `mod: 7` → random modifier in HTML.

The file already sorted keys in the *fallback* paths (with a comment saying
so), but the three normalizing loops above were left unsorted — the review
missed them because each loop is order-independent *unless* two keys
collide after normalization.

## Why it was so rare (~1e-7/exec)
Triggering it needs a ```` ```statblock ```` fence whose YAML body contains
two keys that are distinct to the subset parser but identical after
lowercasing/`-`→`_` folding (e.g. `name:` + `Name:`). The fuzzer must splice
a near-duplicate key line into a statblock body — rare to *generate*, but
once generated the double-parse assertion fails with ~10–45% probability
(map order re-rolls on every `Parse`), which is why CI hit it and short
local runs didn't.

## What was ruled out
- goldmark itself: `PrioritizedSlice.Sort` uses `sort.Slice` (deterministic
  for fixed input order); renderer `Register` overwrites per-`NodeKind` into
  an indexed slice (map iteration order irrelevant); footnote counters walk
  ordered slices; attributes are `[]Attribute` slices; link refs are first-
  wins by parse order; delimiters/IDs live in the fresh per-`Parse`
  `parser.Context`. No `rand`/`time`/`sync.Map`/goroutines in goldmark
  non-test code. Our `newMarkdown()` is fresh per `Parse`; all package vars
  are read-only.
- Frontmatter paths: `parseFrontmatter`/`ValidateFrontmatter` sort keys
  before emitting; `parseBlockMap`/`parseFlowMap` are sequential
  (exact-duplicate keys are deterministic last-wins); admonition params are
  sequential last-wins. No normalization-after-map-iteration anywhere else
  (audited every `for … := range` over a map in `internal/markdown`).

## Repro (minimized)
Any of these rendered 2 distinct HTML outputs within 500 sequential
`Parse` calls before the fix, 1 after:
- `"```statblock\nname: Goblin\nName: Orc\n```\n"` (Goblin vs Orc)
- `"```statblock\nac: 10\nAC: 15\n```\n"`
- `"```statblock\nhit-dice: 1d8\nhit_dice: 2d10\n```\n"`
- `"```statblock\ntraits:\n- name: A\n  NAME: B\n```\n"`
- `"```statblock\nsaves:\n- ability: STR\n  MOD: 5\n  mod: 7\n```\n"`

(`saves:\n  STR: 5\n  str: 7` is deterministic even unfixed — the single-key
fallback path already iterates sorted keys.)

## Fix
Iterate keys in `sort.Strings` order before each normalizing write, keeping
last-wins semantics (winner = last in byte order; pathological duplicates
have no meaningful correct winner, determinism is the requirement).
No contract changes; no new dependencies.

## Regression pins
- `internal/markdown/statblock_test.go: TestStatblockDuplicateKeysDeterministic`
  (5 colliding shapes × 100 sequential parses, winners asserted).
- `internal/markdown/testdata/fuzz/FuzzParse/statblock-dupkey` seed corpus
  entry — fails ~1/3 of runs on the pre-fix code, always passes post-fix,
  so the seed corpus replays it forever.

## Evidence
- `go test ./internal/markdown/ -count=1` green; `go vet`, `gofmt` clean.
- `FUZZ_TIME=120s ./tools/fuzz-short.sh`: PASS (4 targets), FuzzParse
  2,310,882 execs clean.
- `make check` green on the committed tree (templ generate no-op, vet,
  gofmt, golangci-lint, `go test ./...`, budgets).
