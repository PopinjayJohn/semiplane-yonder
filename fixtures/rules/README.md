# `fixtures/rules/` — H1 timebox ruleset packs (dev fixtures, never embedded)

Playable minimum from the SRD 5.2 snapshot in `fixtures/srd/`: one class
(Fighter), one monster (Goblin Warrior), one spell (Fireball). Full
compendium explicitly out (spec §9). Nothing here ships in the binary:
bases/overlays install as vault content under `rules/` (via template
import); a demo vault copies this directory to `<vault>/rules/` (table-local
homebrew may also live at `<vault>/homebrew/`, which shadows an installed
pack of the same id) and records the selection in `campaign.yaml` (see
`campaign.example.yaml`).

## Layout

```
base/dnd/                  Base pack: stats, derived, rolls, dice, identity,
                           dnd intent catalog, one example hook.
  compendium/fighter.md    Playable Fighter (stats + derived + one attack roll)
  compendium/goblin-warrior.md  Goblin Warrior (statblock + one attack roll)
  compendium/fireball.md   Fireball (save + one damage roll)
overlay/5e-2014/           Overlay diff over dnd (2014 values).
overlay/5e-2024/           Overlay diff over dnd (2024 values + Weapon Mastery).
homebrew/grit/             Example homebrew diff (never a fork): one optional
                           + one hook over 5e-2024.
campaign.example.yaml      Frozen campaign.yaml keys, documented.
```

## Rules these fixtures prove (P05)

- Layers compose base → overlay → homebrew; descriptors carry display-only
  versions (recorded, never enforced).
- Optionals are multi-per-file with stable ids (`fighter.md` holds two);
  ids are vault-unique (`internal/campaign.Scan` fails duplicates with
  file:line); GM toggles persist to `enabled-features`.
- Overlay switch (5e-2014 ↔ 5e-2024) changes the resolved stack — and at
  runtime the sheet/rolls — with no rebuild (`Resolve` reads live disk).
- `rules lint` structure checks: unknown keys fail, required keys enforced,
  hook phases/intents shaped, content files must exist. Expression
  *functions* inside formulas are H2's lint, not H1's.

## Consumers

- I1 (unblocked first): copy packs into a scratch vault, `campaign.Load`
  for the setup wizard, `campaign.Scan`/`SortedIDs` for the optionals
  picker, `campaign.SetEnabled` for GM toggles, `campaign.Resolve` for the
  active stack. Wizard sheets go to `characters/<pc>/index.md` (sheet in
  frontmatter); these compendium pages are reference, not sheets.
- H2: `campaign.ValidatePack` is the structure half of `rules lint`; wire
  it and add function-level checks in the evaluator lane.

## Attribution

Fighter, Goblin Warrior, and Fireball excerpts derive from SRD 5.2 and the
Free Rules (2024), Wizards of the Coast, under CC-BY-4.0. Each page keeps
its source line. Homebrew and descriptor prose are original.
