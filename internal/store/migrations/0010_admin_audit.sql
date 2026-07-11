-- Admin audit log: records who did what mutating action through the admin
-- console, and when, so the operator has a trail without needing to read
-- application logs or SQL history directly.

CREATE TABLE admin_audit (
    id INTEGER PRIMARY KEY,
    user_id INTEGER NOT NULL,
    action TEXT NOT NULL,
    target TEXT,
    note TEXT,
    created_at TEXT NOT NULL,
    FOREIGN KEY (user_id) REFERENCES users(id)
) STRICT;

CREATE INDEX idx_admin_audit_created_at ON admin_audit(created_at);
