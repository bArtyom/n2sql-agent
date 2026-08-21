ALTER TABLE document_chunks
    ADD COLUMN heading_search tsvector
    GENERATED ALWAYS AS (
        CASE
            WHEN heading_path <> '' AND heading_path NOT LIKE '% > 第 % 段'
            THEN to_tsvector('simple', heading_path)
            ELSE ''::tsvector
        END
    ) STORED;

CREATE INDEX document_chunks_heading_search_gin_idx
    ON document_chunks USING GIN (heading_search);
