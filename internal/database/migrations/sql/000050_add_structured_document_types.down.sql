ALTER TABLE documents
    DROP CONSTRAINT documents_content_type_check;

ALTER TABLE documents
    ADD CONSTRAINT documents_content_type_check CHECK (
        content_type IN ('text/markdown', 'text/plain', 'application/pdf')
    );
