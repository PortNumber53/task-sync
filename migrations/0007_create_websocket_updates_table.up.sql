-- Create websocket_updates table for queuing real-time updates to be sent via websocket
CREATE TABLE IF NOT EXISTS websocket_updates (
    id SERIAL PRIMARY KEY,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    update_type TEXT NOT NULL, -- e.g., 'rubric_shell', 'step_status', etc.
    task_id INTEGER,           -- nullable, if update is task-specific
    step_id INTEGER,           -- nullable, if update is step-specific
    payload TEXT NOT NULL      -- JSON string with update data
);

-- Index for fast polling by creation time
CREATE INDEX IF NOT EXISTS idx_websocket_updates_created_at ON websocket_updates (created_at);
