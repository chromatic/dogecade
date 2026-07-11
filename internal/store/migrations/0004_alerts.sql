-- Alert records for pool, relay, and chain monitoring.
-- Shared table across pool low-water, relay failures, and reorganization alerts.
-- Clients query unacked alerts (acked_at IS NULL) to drive dashboard warnings.

CREATE TABLE alerts (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL,
    message TEXT NOT NULL,
    created_at TEXT NOT NULL,
    acked_at TEXT
) STRICT;

-- Index on (kind, acked_at) to efficiently query unacked alerts by kind
-- (e.g., "find unacked pool_low_urgent alerts").
CREATE INDEX idx_alerts_kind_acked ON alerts(kind, acked_at);
