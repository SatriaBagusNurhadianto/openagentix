-- Core tables. Idempotent. {{schema}} is a validated, quoted identifier.
CREATE TABLE IF NOT EXISTS {{schema}}.documents (
    collection text        NOT NULL,
    id         text        NOT NULL,
    title      text        NOT NULL DEFAULT '',
    source_uri text        NOT NULL DEFAULT '',
    text       text        NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (collection, id)
);

CREATE TABLE IF NOT EXISTS {{schema}}.chunks (
    collection text    NOT NULL,
    doc_id     text    NOT NULL,
    idx        integer NOT NULL,
    text       text    NOT NULL,
    tsv        tsvector GENERATED ALWAYS AS (to_tsvector('english', text)) STORED,
    PRIMARY KEY (collection, doc_id, idx),
    FOREIGN KEY (collection, doc_id) REFERENCES {{schema}}.documents (collection, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS chunks_tsv_idx ON {{schema}}.chunks USING gin (tsv);
