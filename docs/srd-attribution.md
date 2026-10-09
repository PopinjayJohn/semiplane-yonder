# SRD Attribution (5.2, CC-BY-4.0)

> This page states the license terms for System Reference Document content
> used with Yonder. It is documentation, not legal advice.

## What this covers

Yonder's demo content and ruleset packs draw on the **System Reference
Document 5.2** ("SRD 5.2"), published by Wizards of the Coast under
**Creative Commons Attribution 4.0 International (CC-BY-4.0)**:

- License: https://creativecommons.org/licenses/by/4.0/
- You must give appropriate credit, provide a link to the license, and
  indicate if changes were made.

**Attribution:** This product uses material from SRD 5.2, © Wizards of the
Coast, licensed under CC-BY-4.0. SRD 5.2 excerpts in demo vaults and vault
packs are marked with their source; mechanical conversions (stats, dice,
derived values) derived from SRD 5.2 remain attributed to SRD 5.2 under the
same license.

## What ships where

- **Never in the binary.** The release binary contains ruleset machinery
  only. No SRD text, no Monster Manual, no bundled vault.
- **Demo template.** The demo/tutorial template vaults ship this
  attribution page with the template (not the binary), per P13.
- **Dev fixtures.** `fixtures/srd/` is a user-maintained snapshot used by
  tests and the H1 timebox (Fighter + Goblin + 1 spell). It is never
  embedded in a release.

## What GMs may add privately

The full Monster Manual and non-SRD spells are GM-imported homebrew, never
bundled. `rules lint` **warns** (does not fail) on likely non-SRD monster
text in homebrew — the GM owns table liability. Never ship or redistribute
copyrighted non-SRD text; keep private imports in your own vault, which
`archive-vault`/`clone-vault` carry as your files.

## Version note

The canonical SRD version for this project is **5.2**. Any in-repo text
still saying "5.1" (as of this writing, `fixtures/srd/README.md` line 16)
is stale and FILED for correction — the snapshot content itself
(`Source: SRD 5.2 …` markers across `fixtures/srd/`) is 5.2.
