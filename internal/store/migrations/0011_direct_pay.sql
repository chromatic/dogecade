-- Direct pay-to-machine (Phase 8): per-address use counter for the rotation
-- job's "retire after N uses" trigger, and a DB-level guarantee that a
-- machine has at most one active (state='assigned') direct-pay address at a
-- time -- mirroring idx_addresses_one_assigned_per_user from Phase 4's
-- migration (0006_token_core.sql).

ALTER TABLE addresses ADD COLUMN use_count INTEGER NOT NULL DEFAULT 0;

CREATE UNIQUE INDEX idx_addresses_one_active_per_machine
    ON addresses(machine_id, purpose)
    WHERE state = 'assigned' AND machine_id IS NOT NULL AND purpose = 'machine_direct';
