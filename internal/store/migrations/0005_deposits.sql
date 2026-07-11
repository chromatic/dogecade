-- Deposit records for tracking incoming Dogecoin payments.
-- STRICT tables for type safety.
--
-- A deposit's lifecycle: seen → confirmed → credited (or orphaned if reorged
-- away before crediting). Credited deposits are never clawed back; reorgs raise
-- admin alerts instead.
--
-- block_height is NULL while unconfirmed, and populated once the deposit enters
-- the confirmed state. This is more idiomatic than storing a magic 0 value.
--
-- created_at is deliberately added beyond the design.md sketch to track when
-- a deposit was first seen, matching the pattern of all other tables in the schema.
--
-- crediting_claimed_at guards against double-crediting: the pipeline
-- atomically claims a deposit (UPDATE ... WHERE state='confirmed' AND
-- crediting_claimed_at IS NULL) before calling the credit hook, so a
-- re-delivered chain event or a second confirmation arriving before the
-- hook finishes can't trigger it twice. A stale claim (process crashed
-- mid-credit) is reclaimable after a timeout.

CREATE TABLE deposits (
    id INTEGER PRIMARY KEY,
    address_id INTEGER NOT NULL,
    txid TEXT NOT NULL,
    vout INTEGER NOT NULL,
    amount_koinu INTEGER NOT NULL,
    confirmations INTEGER NOT NULL DEFAULT 0,
    block_height INTEGER,
    state TEXT NOT NULL,
    credited_at TEXT,
    crediting_claimed_at TEXT,
    created_at TEXT NOT NULL,
    CHECK (state IN ('seen', 'confirmed', 'credited', 'orphaned')),
    UNIQUE (txid, vout),
    FOREIGN KEY (address_id) REFERENCES addresses(id)
) STRICT;

-- Index on state for the deposit pipeline to efficiently query "all deposits
-- in state X" (seen, confirmed, etc.) to advance them through the state machine.
CREATE INDEX idx_deposits_state ON deposits(state);

-- Index on address_id for looking up deposits for a given address (e.g., when
-- implementing late-payment rules or address-specific queries).
CREATE INDEX idx_deposits_address_id ON deposits(address_id);
