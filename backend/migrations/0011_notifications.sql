-- Notifications (docs/ARCHITECTURE.md "Notifications"): a person's webhooks
-- and browser (Web Push) subscriptions, the events the store raises and their
-- deliveries. Additive: nothing that exists changes but the jobs' kinds.
--
-- What a channel carries that is a secret (a webhook's address - a Slack or
-- Discord address IS the credential -, its signing secret, a custom header's
-- value; a browser's push endpoint and keys) is sealed with a key derived from
-- FAPPS_DATA_KEY (internal/datakey "notifications"), bound to its row and
-- column; only a hint of the address is kept in the clear for the list.

ALTER TABLE jobs DROP CONSTRAINT jobs_kind_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_kind_check CHECK (kind IN ('resolve', 'dry_run', 'publish_index', 'refresh_index',
                                                                'expire_sessions', 'notify_event', 'notify_deliver'));

-- A webhook: where, in which format, for which events.
CREATE TABLE notification_webhooks (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id       bigint      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    name             text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
    format           text        NOT NULL CHECK (format IN ('generic', 'slack', 'discord', 'teams')),
    url_sealed       bytea       NOT NULL,
    -- scheme://host/... with the path cut: what the list shows.
    url_hint         text        NOT NULL,
    -- generic only: the HMAC-SHA256 key of X-Fapps-Signature.
    secret_sealed    bytea,
    -- generic only, optional: one header of the owner's (an API key).
    header_name      text        CHECK (header_name IS NULL OR char_length(header_name) BETWEEN 1 AND 64),
    header_sealed    bytea,
    events           text[]      NOT NULL DEFAULT '{}',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    -- the last delivery or test: what the list shows.
    last_status      text        CHECK (last_status IN ('delivered', 'failed')),
    last_http_status integer,
    last_error       text,
    last_at          timestamptz,
    CHECK ((header_name IS NULL) = (header_sealed IS NULL)),
    CHECK (format = 'generic' OR (secret_sealed IS NULL AND header_name IS NULL))
);
CREATE INDEX notification_webhooks_account ON notification_webhooks (account_id);

-- A browser's push subscription. endpoint_index is a keyed hash of the
-- endpoint, so the same browser subscribing again finds its row without the
-- endpoint being readable.
CREATE TABLE push_subscriptions (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id       bigint      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    endpoint_index   bytea       NOT NULL UNIQUE CHECK (octet_length(endpoint_index) = 32),
    sealed           bytea       NOT NULL,
    -- the push service's host (fcm.googleapis.com, ...): what the list shows.
    endpoint_host    text        NOT NULL,
    user_agent       text        NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    last_status      text        CHECK (last_status IN ('delivered', 'failed')),
    last_http_status integer,
    last_error       text,
    last_at          timestamptz
);
CREATE INDEX push_subscriptions_account ON push_subscriptions (account_id);

-- Which events a person's browsers show (one set for all of their browsers).
CREATE TABLE notification_preferences (
    account_id  bigint      PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,
    push_events text[]      NOT NULL DEFAULT '{}',
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- An event the store raised. audience: 'admin' (the administrators at
-- delivery time, read again from the administration repository), 'accounts'
-- (account_ids), 'all' (everyone who chose the event). dedupe_key makes an
-- event happen once (a submission's review, a filex version).
CREATE TABLE notification_events (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind        text        NOT NULL,
    audience    text        NOT NULL CHECK (audience IN ('admin', 'accounts', 'all')),
    account_ids bigint[]    NOT NULL DEFAULT '{}',
    dedupe_key  text        UNIQUE,
    data        jsonb       NOT NULL DEFAULT '{}',
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- One event to one channel.
CREATE TABLE notification_deliveries (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_id    bigint      NOT NULL REFERENCES notification_events (id) ON DELETE CASCADE,
    webhook_id  uuid        REFERENCES notification_webhooks (id) ON DELETE CASCADE,
    push_id     uuid        REFERENCES push_subscriptions (id) ON DELETE CASCADE,
    status      text        NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'delivered', 'failed')),
    attempts    integer     NOT NULL DEFAULT 0,
    http_status integer,
    last_error  text,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CHECK ((webhook_id IS NULL) <> (push_id IS NULL)),
    UNIQUE (event_id, webhook_id),
    UNIQUE (event_id, push_id)
);
CREATE INDEX notification_deliveries_event ON notification_deliveries (event_id);

-- Small facts the notifier keeps between runs ('filex_latest': the newest
-- filex release the store has announced).
CREATE TABLE notification_state (
    key        text        PRIMARY KEY,
    value      text        NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
