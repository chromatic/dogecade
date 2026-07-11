-- Create the settings table for storing key-value configuration pairs.
-- STRICT tables enforce column types strictly per SQLite best practices.
CREATE TABLE settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;
