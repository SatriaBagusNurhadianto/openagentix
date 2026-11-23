-- Entity resolution by canonical name (see store.MergeKey). Applied, like every
-- migration, at each start of a graph-enabled kb, so every statement is
-- idempotent. {{schema}} is a validated, quoted identifier.
--
-- Entities used to be merged on (collection, type, norm_name); they are now merged
-- on (collection, canon_name), whatever the type, and keep the other types they
-- were seen with. The canonical form is computed by Go code, so the column is
-- filled by the store (backfillCanonNames) right after this file runs and only
-- then made NOT NULL and unique.
ALTER TABLE {{schema}}.entities ADD COLUMN IF NOT EXISTS canon_name text;
ALTER TABLE {{schema}}.entities ADD COLUMN IF NOT EXISTS other_types text[] NOT NULL DEFAULT '{}';
ALTER TABLE {{schema}}.entities DROP CONSTRAINT IF EXISTS entities_collection_type_norm_name_key;

-- Once, when this migration is first applied: forget what the model extracted
-- under the old merge rule (duplicate entities, one node per spelling) so that
-- documents are extracted again with the current rules on their next ingest.
-- Anything with origin 'manual' stays, and so does what a manual relation still
-- refers to. The version row is written by the caller after this file ran.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM {{schema}}.schema_migrations WHERE version = 5) THEN
    DELETE FROM {{schema}}.relations WHERE origin = 'extracted';
    DELETE FROM {{schema}}.entity_chunks;
    DELETE FROM {{schema}}.extraction_jobs;
    DELETE FROM {{schema}}.entities e
     WHERE e.origin = 'extracted'
       AND NOT EXISTS (SELECT 1 FROM {{schema}}.relations r WHERE r.src_id = e.id OR r.dst_id = e.id);
  END IF;
END
$$;
