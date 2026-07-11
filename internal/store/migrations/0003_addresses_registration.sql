-- Address registration tracking.
-- Adds node_registered_at to track when an address was registered watch-only
-- with the Dogecoin node via importaddress. Nullable to support the "pending
-- import" state: addresses in the pool with node_registered_at=NULL are usable
-- but not yet registered with the node, and a reconciliation job can retry.

ALTER TABLE addresses ADD COLUMN node_registered_at TEXT;
