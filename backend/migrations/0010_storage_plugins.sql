-- 0010_storage_plugins: the store lists filex's other plugin class, storage
-- plugins (docs/ARCHITECTURE.md "Storage plugins"; filex Roadmap #215).
--
-- A storage plugin is a native program, not a sandboxed module: its listing
-- is a release's `filex-storage.json` feed (the manifest of the row), one
-- build per platform pinned by sha256 and signed by the artifact key
-- (`binaries`), and the conformance run the store's plugin validator made of
-- it (`conformance`).
--
-- Additive: an app's and a language pack's rows keep NULL in the new
-- columns, and every query written before this file reads the same rows the
-- same way. The guard of 0002 compares the whole row, so the new columns of an
-- approved version are as immutable as the old ones.

ALTER TABLE apps DROP CONSTRAINT apps_kind_check;
ALTER TABLE apps ADD CONSTRAINT apps_kind_check CHECK (kind IN ('app', 'language_pack', 'storage'));

-- What the publisher submitted: an app or a language pack (read from
-- filex-app.json) or a storage plugin (read from the release's
-- filex-storage.json). Every submission made before this file was the first.
ALTER TABLE submissions
    ADD COLUMN kind text NOT NULL DEFAULT 'app' CHECK (kind IN ('app', 'storage'));

ALTER TABLE app_versions
    -- {"linux/amd64": {"url", "sha256", "size", "sig"}, ...}: every build the
    -- feed names, downloaded and held to its sha256 at review, signed at
    -- approval. NULL for an app or a language pack.
    ADD COLUMN binaries     jsonb CHECK (binaries IS NULL OR jsonb_typeof(binaries) = 'object'),
    -- The plugin validator's run: {"platform", "filex", "verified", "passed",
    -- "failed", "skipped", "driver", "capabilities"}. NULL for an app.
    ADD COLUMN conformance  jsonb CHECK (conformance IS NULL OR jsonb_typeof(conformance) = 'object');

-- A category for them, beside the eight of 0001.
INSERT INTO categories (id, label, position) VALUES ('storage', '{"en": "Storage"}', 90)
    ON CONFLICT (id) DO NOTHING;
