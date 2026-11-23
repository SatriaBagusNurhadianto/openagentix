-- Entity name embeddings; applied only when the graph AND vectors are enabled.
-- {{dim}} is an integer from validated configuration.
ALTER TABLE {{schema}}.entities ADD COLUMN IF NOT EXISTS name_embedding {{vs}}.vector({{dim}});
CREATE INDEX IF NOT EXISTS entities_name_embedding_idx ON {{schema}}.entities USING hnsw (name_embedding {{vs}}.vector_cosine_ops);
