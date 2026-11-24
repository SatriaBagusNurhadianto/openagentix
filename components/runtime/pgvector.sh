#!/usr/bin/env bash
# Installs the pgvector extension into the kagent database, in the schema kagent expects (config.database.vectorSchema,
# default "extensions"), and lets both kagent roles use it. Idempotent; no data is touched. kagent's own migration then
# creates the `memory` table (vector(768) + HNSW index) the first time the controller starts with
# config.database.vectorEnabled=true (values/kagent.yaml). Migrations do NOT create the extension: without this step the
# controller fails its start-up check ("pgvector is not installed in schema ...").
# Needs the roles from dl/kagent-setup.sql (up.sh creates them) and an image that ships pgvector (manifests/kagent-postgres.yaml does).
set -euo pipefail
K=${KUBECTL:-kubectl}
SCHEMA=${KAGENT_VECTOR_SCHEMA:-extensions}
$K -n kagent exec -i kagent-pg-0 -- psql --no-psqlrc -U postgres -d kagent --set=ON_ERROR_STOP=1 --single-transaction --set=vs="$SCHEMA" <<'SQL'
CREATE SCHEMA IF NOT EXISTS :"vs";
CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA :"vs";
GRANT USAGE ON SCHEMA :"vs" TO kagent_owner, kagent_readwrite;
SQL
echo "pgvector: extension 'vector' in schema '$SCHEMA' of database kagent"
