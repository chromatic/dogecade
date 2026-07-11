-- Relay dispatcher schema: relay_boards (Tasmota-flashed ESP8266 boards) and
-- machine_relays (which board/channel fires which machine's coin switch).
--
-- machines already exists (Phase 4, 0006_token_core.sql), so machine_relays
-- can carry a real FOREIGN KEY to it from the start.

CREATE TABLE relay_boards (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    base_url TEXT NOT NULL,
    last_seen_at TEXT,
    is_active INTEGER NOT NULL DEFAULT 1,
    CHECK (is_active IN (0, 1))
) STRICT;

-- relay_number identifies the Tasmota Power{n}/PulseTime{n} channel on the
-- board (Tasmota boards commonly expose multiple relays per board).
CREATE TABLE machine_relays (
    id INTEGER PRIMARY KEY,
    machine_id INTEGER NOT NULL,
    board_id INTEGER NOT NULL,
    relay_number INTEGER NOT NULL,
    is_active INTEGER NOT NULL DEFAULT 1,
    CHECK (is_active IN (0, 1)),
    CHECK (relay_number >= 1),
    FOREIGN KEY (machine_id) REFERENCES machines(id),
    FOREIGN KEY (board_id) REFERENCES relay_boards(id)
) STRICT;

CREATE INDEX idx_machine_relays_machine_id ON machine_relays(machine_id);
CREATE INDEX idx_machine_relays_board_id ON machine_relays(board_id);

-- At most one active relay binding per machine: the dispatcher picks a
-- single board/channel to pulse for a given machine, so a second concurrently
-- active binding would be ambiguous.
CREATE UNIQUE INDEX idx_machine_relays_one_active_per_machine
    ON machine_relays(machine_id)
    WHERE is_active = 1;
