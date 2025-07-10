-- Migration: Create rubric_shell_output_history table
CREATE TABLE rubric_shell_output_history (
    id SERIAL PRIMARY KEY,
    rubric_shell_uuid UUID NOT NULL,
    timestamp TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    criterion TEXT NOT NULL,
    required BOOLEAN NOT NULL,
    score REAL,
    command TEXT NOT NULL,
    solution1_output TEXT,
    solution2_output TEXT,
    solution3_output TEXT,
    solution4_output TEXT,
    module_explanation TEXT,
    exception TEXT
);

CREATE INDEX idx_rubric_shell_uuid ON rubric_shell_output_history (rubric_shell_uuid);
