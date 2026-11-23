-- Knowledge graph tables. Applied only when the graph is enabled. Idempotent.
-- {{schema}} is a validated, quoted identifier.
CREATE TABLE IF NOT EXISTS {{schema}}.entities (
    id          bigint  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    collection  text    NOT NULL,
    name        text    NOT NULL,
    norm_name   text    NOT NULL,           -- merge key: lower case, whitespace collapsed
    match_name  text    NOT NULL,           -- lookup form: lower case, punctuation as space
    type        text    NOT NULL,
    description text    NOT NULL DEFAULT '',
    aliases     text[]  NOT NULL DEFAULT '{}',  -- match form, sorted
    origin      text    NOT NULL DEFAULT 'extracted',
    UNIQUE (collection, type, norm_name)
);

CREATE TABLE IF NOT EXISTS {{schema}}.relations (
    id             bigint  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    collection     text    NOT NULL,
    src_id         bigint  NOT NULL REFERENCES {{schema}}.entities (id) ON DELETE CASCADE,
    dst_id         bigint  NOT NULL REFERENCES {{schema}}.entities (id) ON DELETE CASCADE,
    predicate      text    NOT NULL,
    evidence       text    NOT NULL DEFAULT '',
    document_id    text    NOT NULL,
    chunk_index    integer NOT NULL,
    prompt_version text    NOT NULL DEFAULT '',
    model          text    NOT NULL DEFAULT '',
    origin         text    NOT NULL DEFAULT 'extracted',
    FOREIGN KEY (collection, document_id) REFERENCES {{schema}}.documents (collection, id) ON DELETE CASCADE,
    UNIQUE (collection, src_id, dst_id, predicate, document_id, chunk_index)
);
CREATE INDEX IF NOT EXISTS relations_src_idx ON {{schema}}.relations (src_id);
CREATE INDEX IF NOT EXISTS relations_dst_idx ON {{schema}}.relations (dst_id);
CREATE INDEX IF NOT EXISTS relations_doc_idx ON {{schema}}.relations (collection, document_id);

CREATE TABLE IF NOT EXISTS {{schema}}.entity_chunks (
    entity_id   bigint  NOT NULL REFERENCES {{schema}}.entities (id) ON DELETE CASCADE,
    collection  text    NOT NULL,
    document_id text    NOT NULL,
    chunk_index integer NOT NULL,
    PRIMARY KEY (entity_id, document_id, chunk_index),
    FOREIGN KEY (collection, document_id) REFERENCES {{schema}}.documents (collection, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS entity_chunks_doc_idx ON {{schema}}.entity_chunks (collection, document_id);

-- One row per extraction window. A pending row whose claimed_until is in the
-- past is runnable again (its worker died); not_before delays a retry.
CREATE TABLE IF NOT EXISTS {{schema}}.extraction_jobs (
    collection      text        NOT NULL,
    document_id     text        NOT NULL,
    window_idx      integer     NOT NULL,
    window_hash     text        NOT NULL,
    first_chunk     integer     NOT NULL,
    chunk_count     integer     NOT NULL,
    prompt_version  text        NOT NULL,
    state           text        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'done', 'failed')),
    attempts        integer     NOT NULL DEFAULT 0,
    last_error_code text        NOT NULL DEFAULT '',
    claimed_until   timestamptz,
    not_before      timestamptz,
    created_seq     bigint      GENERATED ALWAYS AS IDENTITY,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (collection, document_id, window_idx),
    FOREIGN KEY (collection, document_id) REFERENCES {{schema}}.documents (collection, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS extraction_jobs_pending_idx ON {{schema}}.extraction_jobs (created_seq) WHERE state = 'pending';
