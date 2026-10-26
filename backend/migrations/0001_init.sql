-- filex Apps store: initial schema (DRAFT - the backend lane owns changes,
-- docs/WORKPLAN.md). PostgreSQL 16. Applied once, in a transaction, by the
-- runner in internal/db; never edited after it has been applied anywhere.
--
-- Shapes mirror backend/internal/domain and api/openapi.yaml. Timestamps are
-- timestamptz (UTC on the wire). Hashes are lower-case hex.

-- ---------------------------------------------------------------- accounts

-- A person. Signs in with one or more identities (GitHub for publishing,
-- Keycloak for reviewing); identities are linked only by an explicit link
-- action while signed in with both, never by a matching e-mail address.
CREATE TABLE accounts (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    display_name    text        NOT NULL,
    email           text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    last_login_at   timestamptz,
    disabled_at     timestamptz,
    disabled_reason text
);

CREATE TABLE identities (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id    bigint      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    provider      text        NOT NULL CHECK (provider IN ('github', 'oidc')),
    -- 'https://github.com' for GitHub, the token's iss for OIDC.
    issuer        text        NOT NULL,
    -- GitHub's numeric user id / the OIDC sub. The login below may change;
    -- this does not.
    subject       text        NOT NULL,
    login         text        NOT NULL,
    email         text,
    -- OIDC: the store roles the token carried at the last sign-in
    -- ('reviewer', 'admin'). Keycloak is the source of truth; this is a
    -- cache for the audit log and the session. Always empty for GitHub.
    roles         text[]      NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz,
    UNIQUE (provider, issuer, subject)
);
CREATE INDEX identities_account ON identities (account_id);

-- Server-side sessions. The cookie carries a random id; only its sha256 is
-- stored. A session belongs to the identity it signed in with: its roles are
-- that identity's.
CREATE TABLE sessions (
    id_hash          bytea       PRIMARY KEY CHECK (octet_length(id_hash) = 32),
    account_id       bigint      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    identity_id      bigint      NOT NULL REFERENCES identities (id) ON DELETE CASCADE,
    csrf_token       text        NOT NULL,
    -- The publisher's GitHub OAuth token, sealed (AEAD) with a key derived
    -- from FAPPS_SESSION_KEY. Used for the repository-access check at
    -- submission time; NULL for OIDC sessions; dropped with the session.
    github_token_sealed bytea,
    created_at       timestamptz NOT NULL DEFAULT now(),
    last_seen_at     timestamptz NOT NULL DEFAULT now(),
    expires_at       timestamptz NOT NULL,
    ip               inet,
    user_agent       text
);
CREATE INDEX sessions_account ON sessions (account_id);
CREATE INDEX sessions_expiry ON sessions (expires_at);

-- ---------------------------------------------------------------- publishers

-- Who stands behind apps. Bound to a GitHub account or organization by its
-- NUMERIC id: a renamed or re-registered login cannot inherit a publisher.
CREATE TABLE publishers (
    id               text        PRIMARY KEY CHECK (id ~ '^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$'),
    name             text        NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    github_owner     text        NOT NULL,
    github_owner_id  bigint      NOT NULL UNIQUE,
    owner_type       text        NOT NULL CHECK (owner_type IN ('User', 'Organization')),
    homepage         text,
    verified         boolean     NOT NULL DEFAULT false,
    official         boolean     NOT NULL DEFAULT false,
    -- Phase 1 is curated: a new publisher waits for an administrator.
    status           text        NOT NULL DEFAULT 'pending'
                                 CHECK (status IN ('pending', 'active', 'suspended')),
    status_reason    text,
    created_by       bigint      REFERENCES accounts (id),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE publisher_members (
    publisher_id text        NOT NULL REFERENCES publishers (id) ON DELETE CASCADE,
    account_id   bigint      NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    role         text        NOT NULL CHECK (role IN ('owner', 'maintainer')),
    added_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (publisher_id, account_id)
);

-- ---------------------------------------------------------------- media

-- Images mirrored by content: the index points at /v1/media/<sha256>.<ext>,
-- never at GitHub. Phase 1 keeps the bytes in the database (backed up with
-- it); blob_key is for a later object store. No SVG (it can carry script).
CREATE TABLE media (
    sha256        text        PRIMARY KEY CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    content_type  text        NOT NULL CHECK (content_type IN ('image/png', 'image/jpeg', 'image/webp')),
    bytes         bigint      NOT NULL CHECK (bytes > 0),
    width         integer     NOT NULL CHECK (width > 0),
    height        integer     NOT NULL CHECK (height > 0),
    source_url    text        NOT NULL,
    blob          bytea,
    blob_key      text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CHECK ((blob IS NULL) <> (blob_key IS NULL))
);

-- ---------------------------------------------------------------- catalog

CREATE TABLE categories (
    id        text    PRIMARY KEY CHECK (id ~ '^[a-z0-9][a-z0-9-]{0,31}$'),
    label     jsonb   NOT NULL,
    position  integer NOT NULL DEFAULT 0
);

-- Names no publisher may take (the store's own words, the host's directory
-- names filex already refuses, and names reserved for official apps).
CREATE TABLE reserved_names (
    name    text PRIMARY KEY,
    reason  text NOT NULL
);

-- One app per repository (filex reads filex-app.json at the repository's
-- root), bound by GitHub's numeric repository id so a rename or a transfer
-- keeps the binding and a new repository under an old name does not get it.
CREATE TABLE apps (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- = the manifest's `name` (filex's own rule: ^[a-z0-9][a-z0-9_-]{0,31}$).
    name            text        NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9][a-z0-9_-]{0,31}$'),
    kind            text        NOT NULL CHECK (kind IN ('app', 'language_pack')),
    publisher_id    text        NOT NULL REFERENCES publishers (id),
    repo_owner      text        NOT NULL,
    repo_name       text        NOT NULL,
    repo_id         bigint      NOT NULL UNIQUE,
    homepage        text,
    license         text,
    categories      text[]      NOT NULL DEFAULT '{}',
    label           jsonb       NOT NULL,
    summary         jsonb       NOT NULL DEFAULT '{}',
    description_md  text,
    icon_sha256     text        REFERENCES media (sha256),
    status          text        NOT NULL DEFAULT 'listed'
                                CHECK (status IN ('listed', 'unlisted', 'revoked')),
    revoked_reason  text,
    revoked_at      timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CHECK ((status = 'revoked') = (revoked_at IS NOT NULL))
);
CREATE INDEX apps_publisher ON apps (publisher_id);

-- The listing's screenshots, replaced from the consent file of each approved
-- version.
CREATE TABLE app_media (
    app_id    bigint  NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    sha256    text    NOT NULL REFERENCES media (sha256),
    position  integer NOT NULL,
    caption   jsonb   NOT NULL DEFAULT '{}',
    PRIMARY KEY (app_id, position)
);

-- ---------------------------------------------------------------- submissions

-- One repository at one tag, on its way through the pipeline.
CREATE TABLE submissions (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    publisher_id     text        NOT NULL REFERENCES publishers (id),
    -- NULL for an app's first submission until it is approved.
    app_id           bigint      REFERENCES apps (id),
    submitted_by     bigint      NOT NULL REFERENCES accounts (id),
    repo             text        NOT NULL CHECK (repo ~ '^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$'),
    ref              text        NOT NULL CHECK (length(ref) BETWEEN 1 AND 255),
    status           text        NOT NULL DEFAULT 'queued'
                                 CHECK (status IN ('queued', 'resolving', 'validating', 'needs_review',
                                                   'approved', 'rejected', 'failed', 'withdrawn')),
    -- domain.Resolution once the resolver has run; name and version are
    -- copied out of it for listing and uniqueness.
    resolution       jsonb,
    name             text,
    version          text,
    publisher_note   text        CHECK (length(publisher_note) <= 4000),
    failure_code     text,
    failure_message  text,
    attempt          integer     NOT NULL DEFAULT 1,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    decided_at       timestamptz,
    decided_by       bigint      REFERENCES accounts (id)
);
-- A repository at a tag is in the pipeline at most once at a time.
CREATE UNIQUE INDEX submissions_one_open ON submissions (lower(repo), ref)
    WHERE status IN ('queued', 'resolving', 'validating', 'needs_review');
CREATE INDEX submissions_status ON submissions (status, created_at);
CREATE INDEX submissions_publisher ON submissions (publisher_id, created_at DESC);

-- Each automated step's outcome, per attempt (a retry is a new attempt).
CREATE TABLE submission_checks (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    submission_id  bigint      NOT NULL REFERENCES submissions (id) ON DELETE CASCADE,
    attempt        integer     NOT NULL,
    kind           text        NOT NULL CHECK (kind IN ('repo_access', 'tag', 'manifest', 'consent', 'policy',
                                                        'artifacts', 'media', 'dry_run')),
    -- dry_run rows: the validator's label ('latest', 'floor', ...).
    validator      text        NOT NULL DEFAULT '',
    status         text        NOT NULL CHECK (status IN ('pending', 'running', 'passed', 'warned',
                                                           'failed', 'skipped')),
    code           text,
    message        text,
    data           jsonb,
    started_at     timestamptz NOT NULL DEFAULT now(),
    ended_at       timestamptz
);
CREATE INDEX submission_checks_submission ON submission_checks (submission_id, attempt);

CREATE TABLE reviews (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    submission_id  bigint      NOT NULL REFERENCES submissions (id) ON DELETE CASCADE,
    reviewer_id    bigint      NOT NULL REFERENCES accounts (id),
    decision       text        NOT NULL CHECK (decision IN ('approve', 'reject', 'comment')),
    note           text        CHECK (length(note) <= 8000),
    -- approve only: the version fixes a security issue (the index's
    -- `security`, which filex uses to stress the update).
    security       boolean     NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reviews_submission ON reviews (submission_id);

-- ---------------------------------------------------------------- keys

-- The public halves of the signing keys. Private keys live in the
-- environment (FAPPS_INDEX_KEY / FAPPS_ARTIFACT_KEY), never here - except a
-- demo's throw-away keys (demo_key_material).
CREATE TABLE signing_keys (
    id            text        PRIMARY KEY CHECK (id ~ '^[a-z0-9][a-z0-9._-]{0,63}$'),
    use           text        NOT NULL CHECK (use IN ('index', 'artifact')),
    public_hex    text        NOT NULL UNIQUE CHECK (public_hex ~ '^[0-9a-f]{64}$'),
    status        text        NOT NULL CHECK (status IN ('next', 'active', 'retired')),
    -- Generated by a demo (FAPPS_DEMO_KEYS=1): no real filex trusts it.
    demo          boolean     NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    activated_at  timestamptz,
    retired_at    timestamptz
);
CREATE UNIQUE INDEX signing_keys_one_active ON signing_keys (use) WHERE status = 'active';

-- ONLY for demo keys; the server refuses to read this table unless APP_ENV
-- is demo or development.
CREATE TABLE demo_key_material (
    key_id  text  PRIMARY KEY REFERENCES signing_keys (id) ON DELETE CASCADE,
    seed    bytea NOT NULL CHECK (octet_length(seed) = 32)
);

-- ---------------------------------------------------------------- versions

-- An approved version. Immutable once written, except the yank fields.
CREATE TABLE app_versions (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    app_id           bigint      NOT NULL REFERENCES apps (id),
    submission_id    bigint      NOT NULL UNIQUE REFERENCES submissions (id),
    version          text        NOT NULL,
    ref              text        NOT NULL,
    commit_sha       text        NOT NULL CHECK (commit_sha ~ '^[0-9a-f]{40}$'),
    filex_range      text        NOT NULL DEFAULT '',
    manifest_url     text        NOT NULL,
    manifest_sha256  text        NOT NULL CHECK (manifest_sha256 ~ '^[0-9a-f]{64}$'),
    manifest_sig     text        NOT NULL,
    manifest         jsonb       NOT NULL,
    wasm_url         text,
    wasm_sha256      text        CHECK (wasm_sha256 ~ '^[0-9a-f]{64}$'),
    wasm_size        bigint,
    wasm_sig         text,
    ui_url           text,
    ui_sha256        text        CHECK (ui_sha256 ~ '^[0-9a-f]{64}$'),
    ui_size          bigint,
    ui_sig           text,
    permissions      text[]      NOT NULL DEFAULT '{}',
    -- filex's own words for each permission (the dry run's rows).
    permission_rows  jsonb       NOT NULL DEFAULT '[]',
    languages        jsonb       NOT NULL DEFAULT '[]',
    engines          text[]      NOT NULL DEFAULT '{}',
    file_types       jsonb,
    notes_url        text,
    security         boolean     NOT NULL DEFAULT false,
    artifact_key_id  text        NOT NULL REFERENCES signing_keys (id),
    published_at     timestamptz NOT NULL DEFAULT now(),
    yanked_reason    text,
    yanked_at        timestamptz,
    yanked_by        bigint      REFERENCES accounts (id),
    UNIQUE (app_id, version),
    CHECK ((wasm_url IS NULL) = (wasm_sha256 IS NULL) AND (wasm_sha256 IS NULL) = (wasm_sig IS NULL)),
    CHECK ((ui_url IS NULL) = (ui_sha256 IS NULL) AND (ui_sha256 IS NULL) = (ui_sig IS NULL)),
    CHECK ((yanked_at IS NULL) = (yanked_reason IS NULL))
);
CREATE INDEX app_versions_app ON app_versions (app_id);

-- ---------------------------------------------------------------- index

-- Every signed index ever served. `serial` only grows (a filex keeps the
-- highest it has seen and refuses a lower one); `body` is the exact bytes
-- that were signed and are served.
CREATE TABLE index_releases (
    serial        bigint      PRIMARY KEY CHECK (serial > 0),
    generated_at  timestamptz NOT NULL,
    expires_at    timestamptz NOT NULL,
    sha256        text        NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    signature     text        NOT NULL,
    key_id        text        NOT NULL REFERENCES signing_keys (id),
    body          bytea       NOT NULL,
    reason        text        NOT NULL CHECK (reason IN ('initial', 'approve', 'yank', 'unyank', 'revoke',
                                                         'unrevoke', 'publisher', 'listing', 'refresh',
                                                         'key_rotation')),
    created_by    bigint      REFERENCES accounts (id),
    CHECK (expires_at > generated_at)
);

-- ---------------------------------------------------------------- jobs

-- The work queue (claimed with FOR UPDATE SKIP LOCKED). Postgres, not Redis:
-- a job must survive a restart and be visible next to the row it works on.
CREATE TABLE jobs (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind          text        NOT NULL CHECK (kind IN ('resolve', 'dry_run', 'publish_index', 'refresh_index',
                                                       'expire_sessions')),
    payload       jsonb       NOT NULL DEFAULT '{}',
    status        text        NOT NULL DEFAULT 'queued'
                              CHECK (status IN ('queued', 'running', 'done', 'failed', 'cancelled')),
    run_after     timestamptz NOT NULL DEFAULT now(),
    attempts      integer     NOT NULL DEFAULT 0,
    max_attempts  integer     NOT NULL DEFAULT 5,
    locked_by     text,
    locked_until  timestamptz,
    last_error    text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX jobs_ready ON jobs (run_after) WHERE status = 'queued';

-- ---------------------------------------------------------------- audit

-- Append-only. Each row carries the hash of the one before it, so a removed
-- or rewritten row shows as a broken chain.
CREATE TABLE audit_log (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at                timestamptz NOT NULL DEFAULT now(),
    actor_account_id  bigint      REFERENCES accounts (id),
    actor_role        text        NOT NULL CHECK (actor_role IN ('anonymous', 'publisher', 'reviewer', 'admin',
                                                                 'system')),
    action            text        NOT NULL,
    target_type       text,
    target_id         text,
    ip                inet,
    data              jsonb       NOT NULL DEFAULT '{}',
    prev_hash         text,
    hash              text        NOT NULL
);
CREATE INDEX audit_log_target ON audit_log (target_type, target_id);
CREATE INDEX audit_log_at ON audit_log (at DESC);

CREATE FUNCTION audit_log_append_only() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only';
END
$$;

CREATE TRIGGER audit_log_no_change
    BEFORE UPDATE OR DELETE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_append_only();

-- ---------------------------------------------------------------- seed data

INSERT INTO categories (id, label, position) VALUES
    ('documents',    '{"en": "Documents"}',          10),
    ('conversion',   '{"en": "Conversion"}',         20),
    ('media',        '{"en": "Media"}',              30),
    ('productivity', '{"en": "Productivity"}',       40),
    ('security',     '{"en": "Security"}',           50),
    ('developer',    '{"en": "Developer tools"}',    60),
    ('integration',  '{"en": "Integrations"}',       70),
    ('fun',          '{"en": "Fun"}',                80);

INSERT INTO reserved_names (name, reason) VALUES
    ('cache',    'filex host directory'),
    ('spool',    'filex host directory'),
    ('public',   'filex host directory'),
    ('assets',   'filex host directory'),
    ('filex',    'the product'),
    ('store',    'the store'),
    ('apps',     'the store'),
    ('admin',    'confusable with the product'),
    ('official', 'confusable with a badge'),
    ('system',   'confusable with the product');
