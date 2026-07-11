-- Track the koinu remainder left over after computing whole tokens from a
-- deposit's amount_koinu (tokens = amount_koinu / token_price_koinu, floored).
-- Recorded for auditability; see the purchase credit hook in
-- internal/services/purchase.go.
ALTER TABLE deposits ADD COLUMN remainder_koinu INTEGER;
