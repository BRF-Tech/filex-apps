-- Paid apps and install links (docs/ARCHITECTURE.md "Licensing" and
-- "Install links").
--
-- Nothing here holds a secret in the clear: a licence key is kept as an
-- HMAC (FAPPS_DATA_KEY, HKDF "license-key") and its first 8 characters; an
-- install intent's token as its sha256; the licence key an intent carries,
-- and a saved instance's address and label, sealed with AES-256-GCM under
-- keys derived from FAPPS_DATA_KEY. Changing that key makes every licence
-- key unknown and every saved instance unreadable.

-- ---------------------------------------------------------------- pricing

-- Display only: the store takes no payment. A free app has no price text and
-- no purchase address.
ALTER TABLE apps ADD COLUMN pricing_model text NOT NULL DEFAULT 'free'
    CHECK (pricing_model IN ('free', 'paid'));
ALTER TABLE apps ADD COLUMN price_text text CHECK (length(price_text) BETWEEN 1 AND 80);
ALTER TABLE apps ADD COLUMN purchase_url text CHECK (purchase_url ~ '^https://' AND length(purchase_url) <= 500);
ALTER TABLE apps ADD CONSTRAINT apps_free_has_no_price
    CHECK (pricing_model = 'paid' OR (price_text IS NULL AND purchase_url IS NULL));

-- ---------------------------------------------------------------- keys

-- A third key use: `license` signs the verdicts of POST /v1/licenses/verify.
-- It is listed in /v1/keys.json, not in the index.
ALTER TABLE signing_keys DROP CONSTRAINT signing_keys_use_check;
ALTER TABLE signing_keys ADD CONSTRAINT signing_keys_use_check CHECK (use IN ('index', 'artifact', 'license'));

-- ---------------------------------------------------------------- licences

CREATE TABLE licenses (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    app_id            bigint      NOT NULL REFERENCES apps (id),
    -- HMAC-SHA256 of the canonical key; the key itself is never stored.
    key_hash          bytea       NOT NULL UNIQUE CHECK (octet_length(key_hash) = 32),
    -- The key's first 8 characters ("FXL-" and four), to tell keys apart.
    key_prefix        text        NOT NULL CHECK (key_prefix ~ '^FXL-[0-9A-HJKMNP-TV-Z]{4}$'),
    licensee_name     text        NOT NULL CHECK (length(licensee_name) BETWEEN 1 AND 120),
    licensee_contact  text        CHECK (length(licensee_contact) BETWEEN 1 AND 200),
    seats             integer     NOT NULL DEFAULT 1 CHECK (seats BETWEEN 1 AND 100000),
    -- Through the end of that UTC day; NULL = no end.
    valid_until       date,
    updates_until     date,
    status            text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    note              text        CHECK (length(note) <= 2000),
    created_by        bigint      REFERENCES accounts (id),
    created_at        timestamptz NOT NULL DEFAULT now(),
    revoked_at        timestamptz,
    revoked_by        bigint      REFERENCES accounts (id),
    CHECK ((status = 'revoked') = (revoked_at IS NOT NULL))
);
CREATE INDEX licenses_app ON licenses (app_id, created_at DESC);

-- The filex instances that checked a licence in. An activation that is not
-- released counts against the seats.
CREATE TABLE license_activations (
    license_id     uuid        NOT NULL REFERENCES licenses (id) ON DELETE CASCADE,
    instance_id    text        NOT NULL CHECK (instance_id ~ '^[!-~]{1,128}$'),
    first_seen     timestamptz NOT NULL DEFAULT now(),
    last_seen      timestamptz NOT NULL DEFAULT now(),
    released_at    timestamptz,
    filex_version  text        CHECK (length(filex_version) <= 64),
    PRIMARY KEY (license_id, instance_id)
);
CREATE INDEX license_activations_active ON license_activations (license_id) WHERE released_at IS NULL;

-- ---------------------------------------------------------------- install intents

-- One install link. The token is in the link only; the table keeps its
-- sha256. Used once, for 30 minutes. The filex it was made for (the signed
-- payload's filex_origin) is sealed like a saved instance's address; the
-- licence key the person added (if any) is sealed with AAD = the intent's
-- id and dropped when the intent is completed or expires.
CREATE TABLE install_intents (
    id                     text        PRIMARY KEY CHECK (id ~ '^[0-9a-f]{16}$'),
    token_hash             bytea       NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    account_id             bigint      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    app_id                 bigint      NOT NULL REFERENCES apps (id),
    version_id             bigint      NOT NULL REFERENCES app_versions (id),
    filex_origin_sealed    bytea       NOT NULL,
    license_id             uuid        REFERENCES licenses (id),
    license_key_sealed     bytea,
    created_at             timestamptz NOT NULL DEFAULT now(),
    expires_at             timestamptz NOT NULL,
    completed_at           timestamptz,
    result                 text        CHECK (result IN ('installed', 'cancelled')),
    completed_instance_id  text        CHECK (completed_instance_id ~ '^[!-~]{1,128}$'),
    CHECK ((completed_at IS NULL) = (result IS NULL)),
    CHECK (license_key_sealed IS NULL OR license_id IS NOT NULL),
    CHECK (expires_at > created_at)
);
CREATE INDEX install_intents_sealed ON install_intents (expires_at) WHERE license_key_sealed IS NOT NULL;
CREATE INDEX install_intents_account ON install_intents (account_id, created_at DESC);

-- ---------------------------------------------------------------- saved instances

-- A person's own filex instances. The address and the label are sealed
-- (AAD binds each to its row and column); url_index is a keyed hash of the
-- account and the address, so a second save of the same address is found
-- without reading any address.
CREATE TABLE saved_instances (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id    bigint      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    url_index     bytea       NOT NULL CHECK (octet_length(url_index) = 32),
    url_sealed    bytea       NOT NULL,
    label_sealed  bytea       NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (account_id, url_index)
);
