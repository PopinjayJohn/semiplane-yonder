-- 0004 (app target): P3 live door states (amend A-VTT2).
--
-- Context (docs/plan/amend-vtt2-pixi-uvtt.md, plan branch
-- plan/vtt-pixi-uvtt): sidecar `vtt-map` portals carry the default
-- open/closed posture (vault truth, survives rebuilds); live GM door
-- toggles land here, keyed by the 0-based position in the sidecar
-- `portals` list.
--
-- Method: purely additive (one new table, no row touched, no table
-- rebuilt). Fresh 0001->0004 chains and upgraded 0003 DBs converge on
-- user_version 4 (runner bumps the version; see internal/store/migrate.go).
-- Non-VTT tables are untouched; the index DB is untouched.
--
-- Ephemeral + fail-closed like vtt_fog: data-dir loss, a missing table,
-- or a missing row renders the door closed (movement-blocking). Readers
-- ignore rows whose idx exceeds the current sidecar portal list (sidecar
-- edits win; see internal/vtt/snapshot.go).
CREATE TABLE IF NOT EXISTS vtt_doors (
    map  TEXT NOT NULL REFERENCES vtt_maps(id) ON DELETE CASCADE,
    idx  INTEGER NOT NULL,
    open INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (map, idx)
);
