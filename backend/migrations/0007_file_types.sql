-- 0007_file_types: the kinds of file each approved version works with and
-- what it does with each (opens, creates, draws thumbnails of, offers an
-- action on, adds a details section to), read from the version's manifest
-- by internal/filetypes. The catalog's file-type search reads it.
--
-- Derived data, not a record: app_versions stays the record (immutable,
-- 0002_guards). A row is written in the approval's own transaction; the
-- versions approved before this migration, and every row an older
-- extractor wrote (`extractor` below internal/filetypes.Extractor), are
-- extracted again from app_versions.manifest at start-up
-- (store.BackfillFileTypes), which may replace a row.

CREATE TABLE app_version_file_types (
    version_id    bigint      PRIMARY KEY REFERENCES app_versions (id),
    extractor     integer     NOT NULL CHECK (extractor > 0),
    -- Every extension the rules name (lower-case, no dot), every media type
    -- (exact or a family like image/*), and whether a rule is offered on
    -- every file.
    exts          text[]      NOT NULL DEFAULT '{}',
    mimes         text[]      NOT NULL DEFAULT '{}',
    any_file      boolean     NOT NULL DEFAULT false,
    -- The rules themselves (filetypes.Rule): role, id, label, kinds, engine,
    -- output.
    rules         jsonb       NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(rules) = 'array'),
    extracted_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX app_version_file_types_exts ON app_version_file_types USING gin (exts);
