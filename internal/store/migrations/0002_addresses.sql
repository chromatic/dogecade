-- Address batch management and pool tracking.
-- STRICT tables for type safety.
--
-- Note: address_batches.loaded_by and addresses.user_id/machine_id are
-- forward references to users/machines tables that don't exist until
-- Phase 4's migration. Unlike CREATE TABLE (which doesn't validate FK
-- target existence), SQLite's FK enforcement (foreign_keys=ON, set by
-- store.Open) requires the referenced table to exist for ANY insert on
-- the child table, even when the FK column value is NULL. So these
-- columns are declared WITHOUT a FOREIGN KEY clause for now; the
-- relationship is an application-level invariant until Phase 4 adds the
-- referenced tables, at which point a later migration can rebuild these
-- tables (SQLite requires a table rebuild to add FK constraints) if
-- enforced referential integrity is wanted.

-- address_batches: audit trail of address batch loads.
CREATE TABLE address_batches (
    id INTEGER PRIMARY KEY,
    source_note TEXT,
    address_count INTEGER NOT NULL,
    loaded_by INTEGER,
    loaded_at TEXT NOT NULL
) STRICT;

-- addresses: core address pool table.
-- Tracks Dogecoin addresses with state (pool|assigned|retired) and purpose
-- (token_deposit|machine_direct). user_id/machine_id are nullable forward
-- references to tables created in later phases (see note above).
CREATE TABLE addresses (
    id INTEGER PRIMARY KEY,
    address TEXT NOT NULL UNIQUE,
    batch_id INTEGER,
    hd_index INTEGER UNIQUE,
    hd_path TEXT,
    state TEXT NOT NULL,
    purpose TEXT NOT NULL,
    user_id INTEGER,
    machine_id INTEGER,
    assigned_at TEXT,
    retired_at TEXT,
    CHECK (state IN ('pool', 'assigned', 'retired')),
    CHECK (purpose IN ('token_deposit', 'machine_direct')),
    FOREIGN KEY (batch_id) REFERENCES address_batches(id)
) STRICT;

-- Index on (state, purpose) to optimize pool queries (e.g., claim oldest
-- available pool address for assignment).
CREATE INDEX idx_addresses_state_purpose ON addresses(state, purpose);

-- hd_cursor: single-row table reserved for libdogecoin backend.
-- Tracks the next HD derivation index. Intentionally left empty until
-- the libdogecoin backend is implemented (deferred phase).
CREATE TABLE hd_cursor (
    next_index INTEGER NOT NULL
) STRICT;
