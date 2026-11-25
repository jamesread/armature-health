-- +migrate Up

-- armature-health v2.x: store text as utf8mb4 whatever the server's default character set,
-- and compare check IDs and issue keys byte for byte. With the default case-insensitive,
-- trailing-space-insensitive collations, "Foo" and "foo " were the same check ID. Apply
-- after health_v2.sql. VARBINARY(764) holds any value the VARCHAR(191) utf8mb4 columns held.
ALTER TABLE health_issues CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
ALTER TABLE health_issues
    MODIFY check_id VARBINARY(764) NOT NULL,
    MODIFY issue_key VARBINARY(764) NOT NULL;

ALTER TABLE health_check_runs CONVERT TO CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
ALTER TABLE health_check_runs MODIFY check_id VARBINARY(764) NOT NULL;

-- +migrate Down

ALTER TABLE health_check_runs MODIFY check_id VARCHAR(191) NOT NULL;
ALTER TABLE health_issues
    MODIFY check_id VARCHAR(191) NOT NULL,
    MODIFY issue_key VARCHAR(191) NOT NULL;
