-- 0002: the database refuses what the code promises never to do.
-- Applied once, in a transaction, by internal/db; never edited afterwards.

-- An approved version is immutable except its yank fields, and never
-- deleted (docs/ARCHITECTURE.md "Data model").
CREATE FUNCTION app_versions_guard() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'app_versions rows are never deleted';
    END IF;
    IF (to_jsonb(NEW) - 'yanked_reason' - 'yanked_at' - 'yanked_by')
       IS DISTINCT FROM (to_jsonb(OLD) - 'yanked_reason' - 'yanked_at' - 'yanked_by') THEN
        RAISE EXCEPTION 'app_versions rows are immutable except the yank fields';
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER app_versions_immutable
    BEFORE UPDATE OR DELETE ON app_versions
    FOR EACH ROW EXECUTE FUNCTION app_versions_guard();

-- Every signed index ever served stays exactly as it was served.
CREATE FUNCTION index_releases_append_only() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'index_releases is append-only';
END
$$;

CREATE TRIGGER index_releases_no_change
    BEFORE UPDATE OR DELETE ON index_releases
    FOR EACH ROW EXECUTE FUNCTION index_releases_append_only();

-- Jobs are found by the submission they work on (withdraw cancels them).
CREATE INDEX jobs_submission ON jobs ((payload ->> 'submission_id'))
    WHERE status IN ('queued', 'running');
