-- One sign-in: GitHub. The store roles come from the account's permission
-- on the administration repository (FAPPS_ADMIN_REPO), read at every sign-in
-- with the server's token (docs/ARCHITECTURE.md "Roles"). The Keycloak
-- sign-in is gone.
--
-- A session now keeps the roles its sign-in established, and why, for its
-- whole life: a later sign-in of the same account elsewhere does not change
-- them (identities.roles would have: a session of the publisher lifetime
-- would have picked up a role granted later). role_source is what /api/v1/me
-- and the audit log say, e.g. 'github repo permission BRF-Tech/filex-market:
-- write'.
ALTER TABLE sessions ADD COLUMN roles text[] NOT NULL DEFAULT '{}'
    CHECK (roles <@ ARRAY['reviewer', 'admin']::text[]);
ALTER TABLE sessions ADD COLUMN role_source text NOT NULL DEFAULT '';

-- Keycloak sessions end here: nothing can start or renew one any more.
DELETE FROM sessions s USING identities i WHERE i.id = s.identity_id AND i.provider = 'oidc';

-- GitHub sessions from before carry no role (they never did).
UPDATE sessions SET role_source = 'not checked: signed in before roles came from GitHub';

-- identities.roles was the Keycloak token's roles at the last sign-in. The
-- old rows keep it as history; nothing writes or reads it any more.
COMMENT ON COLUMN identities.roles IS 'Keycloak sign-ins only (history, until 0005): the roles the token carried. Unused since 0005: a session keeps its own roles (sessions.roles).';
COMMENT ON COLUMN sessions.roles IS 'The store roles this session''s sign-in read from GitHub (permission on FAPPS_ADMIN_REPO); fixed for the session''s life.';
COMMENT ON COLUMN sessions.role_source IS 'Why the session holds its roles, as /api/v1/me and the audit log say it.';
