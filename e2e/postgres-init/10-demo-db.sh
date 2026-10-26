#!/bin/sh
# The database as a hosting platform hands it to an app, for the e2e stack:
# the app's role owns its database and holds nothing more (no superuser, no
# CREATEROLE), so a migration that needs more fails here before it fails on
# such a platform.
set -eu
: "${E2E_DB_PASSWORD:?}"
psql -v ON_ERROR_STOP=1 -v pw="$E2E_DB_PASSWORD" -U postgres -d postgres <<'EOSQL'
CREATE ROLE "demo_fapps" LOGIN;
ALTER ROLE "demo_fapps" WITH PASSWORD :'pw';
CREATE DATABASE "demo_fapps" OWNER "demo_fapps";
GRANT ALL PRIVILEGES ON DATABASE "demo_fapps" TO "demo_fapps";
EOSQL
psql -v ON_ERROR_STOP=1 -U postgres -d demo_fapps -c 'GRANT ALL ON SCHEMA public TO "demo_fapps"'
