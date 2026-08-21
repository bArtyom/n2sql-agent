UPDATE document_parent_chunks
SET content = '结构路径：' || heading_path || E'\n\n' || content,
    heading_path = ''
WHERE heading_path <> '';

UPDATE document_chunks
SET content = '结构路径：' || heading_path || E'\n\n' || content,
    heading_path = ''
WHERE heading_path <> '';
