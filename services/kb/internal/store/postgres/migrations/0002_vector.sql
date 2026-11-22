-- Optional vector support; applied only when an embedding dimension is configured.
-- {{dim}} is an integer from validated configuration.
ALTER TABLE {{schema}}.chunks ADD COLUMN IF NOT EXISTS embedding {{vs}}.vector({{dim}});
CREATE INDEX IF NOT EXISTS chunks_embedding_idx ON {{schema}}.chunks USING hnsw (embedding {{vs}}.vector_cosine_ops);
