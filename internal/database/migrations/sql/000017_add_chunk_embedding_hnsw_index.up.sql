-- Embeddings are currently text-embedding-v4 vectors with a fixed 1024
-- dimensions. Make the dimension explicit before creating HNSW; pgvector
-- cannot build an approximate index over an unbounded vector column.
ALTER TABLE document_chunks
    ALTER COLUMN embedding TYPE vector(1024)
    USING embedding::vector(1024);

CREATE INDEX document_chunks_embedding_hnsw_idx
    ON document_chunks
    USING hnsw (embedding vector_cosine_ops)
    WHERE embedding IS NOT NULL;
