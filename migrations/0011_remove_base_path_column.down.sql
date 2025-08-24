-- Re-add base_path column for rollback (no backfill)
ALTER TABLE tasks ADD COLUMN base_path TEXT;
