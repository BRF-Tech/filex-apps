-- 0009_instance_connections: a filex SERVER asks for install intents in its
-- own name (docs/ARCHITECTURE.md "Instance connections"; filex Roadmap #163,
-- the store side of filex's embedded store screen, #162).
--
-- A person connects one of their saved instances to that filex: the store
-- makes a one-time connection code (its sha256 kept here, never the code),
-- the filex's administrator pastes it into filex, and the filex sends the
-- code with the public half of an ed25519 key it made for this store. From
-- then on the filex signs its requests with that key; the store keeps only
-- the public key and its fingerprint. A filex that disconnects, or a person
-- who disconnects it here, clears the key.
--
-- Additive: the code before it never reads these columns or tables.

ALTER TABLE saved_instances
    -- The filex's public key (32 raw bytes) and its fingerprint (lower-hex
    -- sha256 of those bytes: what a request names its key by).
    ADD COLUMN server_key              bytea       CHECK (server_key IS NULL OR octet_length(server_key) = 32),
    ADD COLUMN server_key_fingerprint  text        CHECK (server_key_fingerprint ~ '^[0-9a-f]{64}$'),
    -- The filex's own opaque id (what it counts license seats by), as it
    -- reported it when it connected; "" is NULL.
    ADD COLUMN filex_instance_id       text        CHECK (filex_instance_id ~ '^[!-~]{1,128}$'),
    ADD COLUMN connected_at            timestamptz,
    ADD COLUMN last_request_at         timestamptz,
    ADD CONSTRAINT saved_instances_connection_whole
        CHECK ((server_key IS NULL) = (server_key_fingerprint IS NULL) AND (server_key IS NULL) = (connected_at IS NULL));

-- One key, one instance: a key is found by its fingerprint.
CREATE UNIQUE INDEX saved_instances_server_key ON saved_instances (server_key_fingerprint)
    WHERE server_key_fingerprint IS NOT NULL;

-- Connection codes: 32 random bytes, shown once, valid 30 minutes, spent by
-- the first connection.
CREATE TABLE instance_connect_codes (
    code_hash    bytea       PRIMARY KEY CHECK (octet_length(code_hash) = 32),
    instance_id  uuid        NOT NULL REFERENCES saved_instances (id) ON DELETE CASCADE,
    account_id   bigint      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    used_at      timestamptz,
    CHECK (expires_at > created_at)
);
CREATE INDEX instance_connect_codes_instance ON instance_connect_codes (instance_id);

-- The nonces of signed requests seen in the last minutes: a request carried
-- again is refused (its timestamp keeps it inside the window, this table
-- keeps it single).
CREATE TABLE instance_request_nonces (
    instance_id  uuid        NOT NULL REFERENCES saved_instances (id) ON DELETE CASCADE,
    nonce        text        NOT NULL CHECK (nonce ~ '^[A-Za-z0-9_-]{16,64}$'),
    seen_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (instance_id, nonce)
);
CREATE INDEX instance_request_nonces_seen ON instance_request_nonces (seen_at);

-- Where an intent was asked for: the store's web interface (a person signed
-- in) or a connected filex server (its saved instance).
ALTER TABLE install_intents
    ADD COLUMN source                text NOT NULL DEFAULT 'web' CHECK (source IN ('web', 'instance')),
    ADD COLUMN requested_by_instance uuid REFERENCES saved_instances (id) ON DELETE SET NULL;
