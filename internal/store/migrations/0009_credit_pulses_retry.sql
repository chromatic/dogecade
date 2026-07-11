-- Track when a failed dispatch attempt should next be retried, so the relay
-- dispatcher can apply backoff between attempts instead of hammering a
-- struggling board every poll cycle. NULL means eligible for immediate
-- (re)dispatch.
ALTER TABLE credit_pulses ADD COLUMN next_attempt_at TEXT;
