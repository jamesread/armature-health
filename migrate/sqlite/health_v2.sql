-- +migrate Up

-- armature-health v2: app-defined issue attributes, and acknowledging users identified by
-- a string (username, UUID, ...) rather than an integer id. Apply after health.sql.

ALTER TABLE health_issues ADD COLUMN attributes TEXT NOT NULL DEFAULT '{}';

ALTER TABLE health_issues ADD COLUMN acknowledged_by_v2 TEXT;
UPDATE health_issues SET acknowledged_by_v2 = CAST(acknowledged_by AS TEXT) WHERE acknowledged_by IS NOT NULL;
ALTER TABLE health_issues DROP COLUMN acknowledged_by;
ALTER TABLE health_issues RENAME COLUMN acknowledged_by_v2 TO acknowledged_by;

-- +migrate Down

-- Acknowledgements by non-numeric user ids cannot be kept as integers; they are cleared.
ALTER TABLE health_issues ADD COLUMN acknowledged_by_v1 INTEGER;
UPDATE health_issues SET acknowledged_by_v1 = CAST(acknowledged_by AS INTEGER)
    WHERE acknowledged_by IS NOT NULL AND acknowledged_by GLOB '[0-9]*' AND acknowledged_by NOT GLOB '*[^0-9]*';
UPDATE health_issues SET acknowledged_at = NULL WHERE acknowledged_by_v1 IS NULL;
ALTER TABLE health_issues DROP COLUMN acknowledged_by;
ALTER TABLE health_issues RENAME COLUMN acknowledged_by_v1 TO acknowledged_by;

ALTER TABLE health_issues DROP COLUMN attributes;
