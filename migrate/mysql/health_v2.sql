-- +migrate Up

-- armature-health v2: app-defined issue attributes, and acknowledging users identified by
-- a string (username, UUID, ...) rather than an integer id. Apply after health.sql.

-- TEXT columns cannot have a default on older MySQL, so add, fill, then make NOT NULL.
ALTER TABLE health_issues ADD COLUMN attributes TEXT NULL;
UPDATE health_issues SET attributes = '{}';
ALTER TABLE health_issues MODIFY attributes TEXT NOT NULL;

-- Existing integer ids become their decimal strings.
ALTER TABLE health_issues MODIFY acknowledged_by VARCHAR(191) NULL;

-- +migrate Down

-- Acknowledgements by non-numeric user ids cannot be kept as integers; they are cleared.
UPDATE health_issues SET acknowledged_at = NULL, acknowledged_by = NULL
    WHERE acknowledged_by IS NOT NULL AND acknowledged_by NOT REGEXP '^[0-9]+$';
ALTER TABLE health_issues MODIFY acknowledged_by BIGINT NULL;

ALTER TABLE health_issues DROP COLUMN attributes;
