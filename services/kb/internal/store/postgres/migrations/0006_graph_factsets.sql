-- Curated fact sets. Applied, like every migration, at each start of a
-- graph-enabled kb, so every statement is idempotent and keeps existing rows.
-- {{schema}} is a validated, quoted identifier.
--
-- A fact set is a named, operator-written catalogue of entities and relations of
-- one collection. Its body is kept as supplied (normalised) to give it back and
-- to tell an identical upload from a changed one. The entities it declares are
-- recorded in graph_factset_entities: an entity is manual (origin 'manual')
-- exactly while some fact set declares it.
CREATE TABLE IF NOT EXISTS {{schema}}.graph_factsets (
    collection     text        NOT NULL,
    id             text        NOT NULL,
    source_uri     text        NOT NULL DEFAULT '',
    body_hash      text        NOT NULL,
    body           jsonb       NOT NULL,
    entity_count   integer     NOT NULL,
    relation_count integer     NOT NULL,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (collection, id)
);

CREATE TABLE IF NOT EXISTS {{schema}}.graph_factset_entities (
    collection text   NOT NULL,
    factset_id text   NOT NULL,
    entity_id  bigint NOT NULL REFERENCES {{schema}}.entities (id) ON DELETE CASCADE,
    PRIMARY KEY (collection, factset_id, entity_id),
    FOREIGN KEY (collection, factset_id) REFERENCES {{schema}}.graph_factsets (collection, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS graph_factset_entities_entity_idx ON {{schema}}.graph_factset_entities (entity_id);

-- A relation cites either a document chunk or a fact set. Existing rows all
-- cite a document and stay valid.
ALTER TABLE {{schema}}.relations ALTER COLUMN document_id DROP NOT NULL;
ALTER TABLE {{schema}}.relations ALTER COLUMN chunk_index DROP NOT NULL;
ALTER TABLE {{schema}}.relations ADD COLUMN IF NOT EXISTS factset_id text;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'relations_factset_fk' AND conrelid = '{{schema}}.relations'::regclass) THEN
    ALTER TABLE {{schema}}.relations ADD CONSTRAINT relations_factset_fk
      FOREIGN KEY (collection, factset_id) REFERENCES {{schema}}.graph_factsets (collection, id) ON DELETE CASCADE;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'relations_one_source' AND conrelid = '{{schema}}.relations'::regclass) THEN
    ALTER TABLE {{schema}}.relations ADD CONSTRAINT relations_one_source CHECK (
      (document_id IS NOT NULL AND chunk_index IS NOT NULL AND factset_id IS NULL)
      OR (document_id IS NULL AND chunk_index IS NULL AND factset_id IS NOT NULL));
  END IF;
END
$$;
-- The unique key of the document relations does not apply to rows without a
-- document (NULLs are distinct), so a fact set has its own.
CREATE UNIQUE INDEX IF NOT EXISTS relations_factset_key ON {{schema}}.relations (collection, factset_id, src_id, dst_id, predicate) WHERE factset_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS relations_factset_idx ON {{schema}}.relations (collection, factset_id) WHERE factset_id IS NOT NULL;
