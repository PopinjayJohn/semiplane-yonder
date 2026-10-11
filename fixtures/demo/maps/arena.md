---
title: Ashen Ford
vtt-map:
  grid: {cols: 20, rows: 14}
  fog-default: hidden
  tokens:
    - {id: mira-pc, name: Mira, x: 3, y: 4, hp: 13, max-hp: 13, character: characters/mira/index.md}
    - {id: bram-pc, name: Bram, x: 5, y: 4, hp: 11, max-hp: 11, character: characters/bram/index.md}
    - {id: goblin-ambush, name: Goblin, x: 15, y: 8, hp: 7, max-hp: 7, hidden: true}
---

# Ashen Ford

Encounter sidecar for the ash-plain ford where [[welcome|Ashfall]]'s east road
crosses the river. Open (non-secret) on purpose: every logged-in player may
open the table, while the goblin in the reed bank stays `hidden: true` and is
filtered out of every non-GM view server-side (fail-closed, per Lane K).

This page holds calibration only — grid, fog posture, and starting token
defaults. Live token positions, fog, and initiative live in the app DB
(`vtt_tokens` / `vtt_fog` / `vtt_initiative`) and are rebuilt from these
defaults if the data dir is ever lost, with fog **hidden** (never revealed).

No background image is set. Drop one at `assets/maps/ford.png` and add
`background: assets/maps/ford.png` here when the GM has art — the board
serves it through the ACL-checked `/assets` handler, never raw.

Map id is `arena`, so the table lives at `/vtt/arena` (title and id need not
match).

## First time on the table

Live state lives in the app DB, not this page, so a fresh install starts with
an empty board. A GM opening `/vtt/arena` clicks **Reset to sidecar** once
(Fog controls) to seed the tokens + fog posture above into the live state.
Players never see that control. After that, moves, HP, fog, and initiative
persist in the app DB — and if the data dir is ever lost, resetting again
rebuilds from this page with fog **hidden**.
