-- 0008_conversions: which kinds of file a version converts into which, as
-- its listing declares them (`conversions` in .filex-apps.json,
-- schema/filex-apps.schema.json, held to the schema and to the manifest's
-- engines by internal/consent). The catalog's file-type search reads it next
-- to app_version_file_types (internal/filetypes, role "convert"): a search
-- for .docx finds an app whose conversions start from docx.
--
-- A record, unlike app_version_file_types: it is not derived from the
-- manifest (an action offered on every file names no kind), so no extractor
-- reads it again. Written in the approval's own transaction from the listing
-- reviewed with the code (source 'listing': the commit's .filex-apps.json,
-- or an operator import's listing), or set afterwards on an official,
-- verified publisher's version by `fapps-server conversions` (source
-- 'operator', with an audit row 'version.conversions'). Not in the signed
-- index: filex does not read it, so a change here publishes nothing.
--
-- Additive: the code before it never reads this table.

CREATE TABLE app_version_conversions (
    version_id   bigint      PRIMARY KEY REFERENCES app_versions (id),
    -- filetypes.Conversion entries: {"from": [...], "to": [...], "needs": [...]}.
    conversions  jsonb       NOT NULL CHECK (jsonb_typeof(conversions) = 'array' AND jsonb_array_length(conversions) > 0),
    source       text        NOT NULL CHECK (source IN ('listing', 'operator')),
    -- Where the declaration was read: the repository and commit, or the
    -- operator's listing file and its sha256.
    origin       text        NOT NULL DEFAULT '',
    declared_at  timestamptz NOT NULL DEFAULT now()
);
