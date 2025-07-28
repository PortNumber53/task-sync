ALTER TABLE tasks ADD COLUMN base_path TEXT;
UPDATE tasks SET base_path = local_path;
