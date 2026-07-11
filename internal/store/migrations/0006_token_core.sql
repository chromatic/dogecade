-- Token core: users, machines, credit_pulses, token_ledger.
--
-- addresses.user_id/machine_id and address_batches.loaded_by (added in Phase 2,
-- see 0002_addresses.sql) stay plain nullable columns without a FOREIGN KEY
-- clause even though users/machines now exist: SQLite has no ALTER TABLE ...
-- ADD CONSTRAINT, so adding the FK here would mean rebuilding those tables
-- (copy, drop, recreate, reinsert). There's no production data yet, but the
-- relationship is simple and already documented as an application-level
-- invariant (see design.md's data-model note), so we leave it as-is rather
-- than doing a rebuild for marginal benefit.

CREATE TABLE users (
    id INTEGER PRIMARY KEY,
    subject_hash TEXT NOT NULL UNIQUE,
    display_name TEXT,
    is_admin INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    CHECK (is_admin IN (0, 1))
) STRICT;

CREATE TABLE machines (
    id INTEGER PRIMARY KEY,
    slug TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    direct_pay_enabled INTEGER NOT NULL DEFAULT 0,
    direct_play_price_koinu INTEGER,
    is_active INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    CHECK (direct_pay_enabled IN (0, 1)),
    CHECK (is_active IN (0, 1))
) STRICT;

-- credit_pulses is created before token_ledger so token_ledger.pulse_id can
-- carry a real FOREIGN KEY reference to it.
CREATE TABLE credit_pulses (
    id INTEGER PRIMARY KEY,
    machine_id INTEGER NOT NULL,
    user_id INTEGER,
    source TEXT NOT NULL,
    state TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    created_at TEXT NOT NULL,
    sent_at TEXT,
    CHECK (source IN ('token_redemption', 'direct_pay')),
    CHECK (state IN ('pending', 'sent', 'failed')),
    FOREIGN KEY (machine_id) REFERENCES machines(id),
    FOREIGN KEY (user_id) REFERENCES users(id)
) STRICT;

CREATE INDEX idx_credit_pulses_state ON credit_pulses(state);
CREATE INDEX idx_credit_pulses_machine_id ON credit_pulses(machine_id);

-- token_ledger: append-only balance entries. A user's balance is
-- SUM(delta) over their rows; rows are never updated or deleted, only
-- inserted, so the ledger doubles as a full audit trail.
CREATE TABLE token_ledger (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL,
    delta INTEGER NOT NULL,
    kind TEXT NOT NULL,
    deposit_id INTEGER,
    pulse_id INTEGER,
    note TEXT,
    created_at TEXT NOT NULL,
    CHECK (kind IN ('purchase', 'redemption', 'refund', 'admin_adjust')),
    FOREIGN KEY (user_id) REFERENCES users(id),
    FOREIGN KEY (deposit_id) REFERENCES deposits(id),
    FOREIGN KEY (pulse_id) REFERENCES credit_pulses(id)
) STRICT;

CREATE INDEX idx_token_ledger_user_id ON token_ledger(user_id);

-- Enforce at most one in-flight ("assigned") token_deposit address per user
-- at the database level: PurchaseService.StartPurchase reuses an existing
-- assigned address instead of creating a second one when a customer taps
-- "buy" twice before paying, and this index makes that a hard invariant
-- rather than just an application convention.
CREATE UNIQUE INDEX idx_addresses_one_assigned_per_user
    ON addresses(user_id, purpose)
    WHERE state = 'assigned' AND user_id IS NOT NULL;
