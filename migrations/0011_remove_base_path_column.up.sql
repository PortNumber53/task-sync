-- Drop base_path column from tasks; local_path is the single source of truth
ALTER TABLE tasks DROP COLUMN IF EXISTS base_path;
