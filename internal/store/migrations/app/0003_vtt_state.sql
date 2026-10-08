-- 0003 (app target): Lane K VTT display state (Phase 4 amend).
--
-- Context (docs/plan/p08.md, Lane K): 0001 created the VTT live-state tables
-- with position-only tokens (map, token, x, y, hidden). The VTT MVP needs
-- board-visible display columns (name, HP, linked character page) plus the
-- initiative order the encounter-spawn intent writes in one action.
--
-- Method: purely additive. Existing rows keep working (new columns carry
-- sane defaults: unnamed, 0/0 HP, no character link). No table is rebuilt,
-- no row is touched. Fresh 0001->0003 chains and upgraded 0002 DBs converge
-- on user_version 3 (runner bumps the version; see internal/store/migrate.go).
-- vtt_maps.calibration and vtt_fog.mask semantics are unchanged (Lane K
-- interprets them; missing/empty fog rows still mean fully hidden).
-- Non-VTT tables are untouched.

-- Display columns for board tokens (HP is board-visible live state, owned by
-- Lane K; the character sheet in characters/<pc>/index.md stays truth for
-- sheet data and is only linked via `character`, never copied).
ALTER TABLE vtt_tokens ADD COLUMN name TEXT NOT NULL DEFAULT '';
ALTER TABLE vtt_tokens ADD COLUMN hp INTEGER NOT NULL DEFAULT 0;
ALTER TABLE vtt_tokens ADD COLUMN max_hp INTEGER NOT NULL DEFAULT 0;
ALTER TABLE vtt_tokens ADD COLUMN character TEXT NOT NULL DEFAULT '';

-- Initiative order per map (Lane K writes it with the encounter-spawn
-- intent in one action; the initiative-tracker feature re-sorts later).
CREATE TABLE IF NOT EXISTS vtt_initiative (
    map TEXT NOT NULL,
    token TEXT NOT NULL,
    ord INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (map, token)
);
