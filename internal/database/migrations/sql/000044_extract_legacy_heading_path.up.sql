UPDATE document_parent_chunks
SET heading_path = substring(content FROM '^结构路径：([^\r\n]+)'),
    content = regexp_replace(content, '^结构路径：[^\r\n]*(\r?\n){2}', '')
WHERE heading_path = ''
  AND content ~ '^结构路径：[^\r\n]+(\r?\n){2}';

UPDATE document_chunks
SET heading_path = substring(content FROM '^结构路径：([^\r\n]+)'),
    content = regexp_replace(content, '^结构路径：[^\r\n]*(\r?\n){2}', '')
WHERE heading_path = ''
  AND content ~ '^结构路径：[^\r\n]+(\r?\n){2}';
