package documentchunk

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bArtyom/n2sql-agent/internal/documentsummary"
)

// ReadSummaryContent returns ordered parent chunks for the dedicated document
// summary pipeline. It intentionally bypasses Agent tool-step limits.
func (s *PostgresStore) ReadSummaryContent(ctx context.Context, knowledgeBaseID, documentID int64) (documentsummary.Document, error) {
	var filename string
	if err := s.db.QueryRowContext(ctx, `
		SELECT d.original_filename FROM documents AS d
		JOIN knowledge_bases AS kb ON kb.id = d.knowledge_base_id
		WHERE d.id = $1 AND d.knowledge_base_id = $2
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, documentID, knowledgeBaseID).Scan(&filename); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return documentsummary.Document{}, ErrChunkNotFound
		}
		return documentsummary.Document{}, fmt.Errorf("read summary document: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT content FROM document_parent_chunks
		WHERE document_id = $1 ORDER BY position`, documentID)
	if err != nil {
		return documentsummary.Document{}, fmt.Errorf("read summary parents: %w", err)
	}
	defer rows.Close()
	chunks := make([]string, 0)
	for rows.Next() {
		var content string
		if err := rows.Scan(&content); err != nil {
			return documentsummary.Document{}, fmt.Errorf("scan summary parent: %w", err)
		}
		chunks = append(chunks, content)
	}
	if err := rows.Err(); err != nil {
		return documentsummary.Document{}, fmt.Errorf("iterate summary parents: %w", err)
	}
	if len(chunks) == 0 {
		rows, err := s.db.QueryContext(ctx, `SELECT content FROM document_chunks WHERE document_id = $1 AND chunk_kind = 'text' ORDER BY position`, documentID)
		if err != nil {
			return documentsummary.Document{}, fmt.Errorf("read summary chunks: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var content string
			if err := rows.Scan(&content); err != nil {
				return documentsummary.Document{}, fmt.Errorf("scan summary chunk: %w", err)
			}
			chunks = append(chunks, content)
		}
		if err := rows.Err(); err != nil {
			return documentsummary.Document{}, fmt.Errorf("iterate summary chunks: %w", err)
		}
	}
	return documentsummary.Document{Filename: filename, Chunks: chunks}, nil
}

var ErrChunkNotFound = errors.New("document chunk not found")

type Splitter struct{ size, overlap int }

func NewSplitter(size, overlap int) *Splitter { return &Splitter{size: size, overlap: overlap} }

func (s *Splitter) Split(text string) []string {
	if s.size <= 0 || strings.TrimSpace(text) == "" {
		return nil
	}
	runes := []rune(text)
	var chunks []string
	for start := 0; start < len(runes); {
		end := start + s.size
		if end > len(runes) {
			end = len(runes)
		}
		if end < len(runes) {
			for i := end - 1; i > start; i-- {
				if runes[i] == '\n' && i+1 < len(runes) && runes[i+1] == '\n' {
					end = i
					break
				}
			}
		}
		chunk := strings.TrimSpace(string(runes[start:end]))
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		if end == len(runes) {
			break
		}
		next := end - s.overlap
		if next <= start {
			next = end
		}
		start = next
	}
	return chunks
}

type Store interface {
	Replace(context.Context, int64, []string, [][]float32) error
}

// Reader loads one chunk for a user-visible citation. Implementations must
// enforce the knowledge-base boundary before returning content.
type Reader interface {
	Read(context.Context, int64, int64, int) (SearchResult, error)
}

// KindReader optionally reads a citation while preserving whether it is a
// 正文 chunk or a generated document summary.
type KindReader interface {
	ReadKind(context.Context, int64, int64, int, string) (SearchResult, error)
}

// RangeReader loads a bounded sequence of chunks for document preview or a
// document-reading tool. Implementations must enforce the same knowledge-base
// and administrator boundary as Reader.
type RangeReader interface {
	ReadRange(context.Context, int64, int64, int, int, int) (RangeResult, error)
}

type DiagnosticsReader interface {
	ChunkingDiagnostics(context.Context, int64, int64) (SplitDiagnostics, error)
}

type RangeResult struct {
	Chunks       []SearchResult `json:"chunks"`
	NextPosition int            `json:"nextPosition"`
	Truncated    bool           `json:"truncated"`
}

type ParentChunk struct {
	Position    int
	Content     string
	HeadingPath string
}

type ChildChunk struct {
	Position       int
	ParentPosition int
	Content        string
	HeadingPath    string
}

// ChunkReference identifies one child chunk in a document. It is used by
// retrieval when loading several parent chunks in one database query.
type ChunkReference struct {
	DocumentID int64
	Position   int
}

type SearchResult struct {
	DocumentID        int64          `json:"documentId"`
	OriginalFilename  string         `json:"originalFilename,omitempty"`
	AssetURL          string         `json:"assetUrl,omitempty"`
	AssetURLs         []string       `json:"assetUrls,omitempty"`
	Position          int            `json:"position"`
	Content           string         `json:"content"`
	ChunkKind         string         `json:"chunkKind,omitempty"`
	HeadingPath       string         `json:"headingPath,omitempty"`
	ParentContent     string         `json:"parentContent,omitempty"`
	ParentPosition    int            `json:"parentPosition,omitempty"`
	ContextBefore     []ContextChunk `json:"contextBefore,omitempty"`
	ContextAfter      []ContextChunk `json:"contextAfter,omitempty"`
	Distance          float64        `json:"distance"`
	MatchType         string         `json:"matchType,omitempty"`
	KeywordScore      float64        `json:"keywordScore,omitempty"`
	HeadingScore      float64        `json:"headingScore,omitempty"`
	KeywordScoreKnown bool           `json:"-"`
	FusionScore       float64        `json:"fusionScore,omitempty"`
	RerankScore       float64        `json:"rerankScore,omitempty"`
}

// ContextChunk is a nearby chunk loaded after retrieval. It stays separate
// from Content so callers can distinguish the original hit from its context.
type ContextChunk struct {
	Position int    `json:"position"`
	Content  string `json:"content"`
}

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) AssetURLs(ctx context.Context, knowledgeBaseID, documentID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id
		FROM document_assets AS a
		JOIN documents AS d ON d.id = a.document_id
		JOIN knowledge_bases AS kb ON kb.id = d.knowledge_base_id
		WHERE a.document_id = $1 AND d.knowledge_base_id = $2
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		ORDER BY a.asset_index, a.id`, documentID, knowledgeBaseID)
	if err != nil {
		return nil, fmt.Errorf("list document asset URLs: %w", err)
	}
	defer rows.Close()
	urls := make([]string, 0)
	for rows.Next() {
		var assetID int64
		if err := rows.Scan(&assetID); err != nil {
			return nil, fmt.Errorf("scan document asset URL: %w", err)
		}
		urls = append(urls, fmt.Sprintf("/api/knowledge-bases/%d/documents/%d/assets/%d", knowledgeBaseID, documentID, assetID))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate document asset URLs: %w", err)
	}
	return urls, nil
}

func (s *PostgresStore) SaveChunkingDiagnostics(ctx context.Context, documentID int64, diagnostics SplitDiagnostics) error {
	payload, err := json.Marshal(diagnostics)
	if err != nil {
		return fmt.Errorf("encode chunking diagnostics: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE documents SET chunking_diagnostics = $2::jsonb WHERE id = $1`, documentID, payload)
	if err != nil {
		return fmt.Errorf("save chunking diagnostics: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrChunkNotFound
	}
	return nil
}

func (s *PostgresStore) ChunkingDiagnostics(ctx context.Context, knowledgeBaseID, documentID int64) (SplitDiagnostics, error) {
	var payload []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT d.chunking_diagnostics
		FROM documents AS d
		JOIN knowledge_bases AS kb ON kb.id = d.knowledge_base_id
		WHERE d.id = $1 AND d.knowledge_base_id = $2
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, documentID, knowledgeBaseID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return SplitDiagnostics{}, ErrChunkNotFound
	}
	if err != nil {
		return SplitDiagnostics{}, fmt.Errorf("read chunking diagnostics: %w", err)
	}
	var diagnostics SplitDiagnostics
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &diagnostics); err != nil {
			return SplitDiagnostics{}, fmt.Errorf("decode chunking diagnostics: %w", err)
		}
	}
	return diagnostics, nil
}

// Read loads one chunk for a citation detail view. The knowledge-base and
// current-administrator predicates are part of the SQL query so a caller
// cannot use a document/position pair to cross the retrieval boundary.
func (s *PostgresStore) Read(ctx context.Context, knowledgeBaseID, documentID int64, position int) (SearchResult, error) {
	return s.ReadKind(ctx, knowledgeBaseID, documentID, position, "text")
}

func (s *PostgresStore) ReadKind(ctx context.Context, knowledgeBaseID, documentID int64, position int, kind string) (SearchResult, error) {
	if knowledgeBaseID <= 0 || documentID <= 0 || position < 0 {
		return SearchResult{}, ErrChunkNotFound
	}
	if kind != "text" && kind != "summary" {
		return SearchResult{}, ErrChunkNotFound
	}
	var result SearchResult
	var chunkKind string
	var headingPath string
	var parentContent sql.NullString
	var parentPosition sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT chunks.document_id, documents.original_filename, chunks.position,
		       chunks.content, chunks.chunk_kind, chunks.heading_path, parents.content, parents.position
		FROM document_chunks AS chunks
		JOIN documents ON documents.id = chunks.document_id
		JOIN knowledge_bases AS kb ON kb.id = documents.knowledge_base_id
		LEFT JOIN document_parent_chunks AS parents ON parents.id = chunks.parent_chunk_id
		WHERE documents.knowledge_base_id = $1
		  AND documents.id = $2
		  AND chunks.position = $3
		  AND chunks.chunk_kind = $4
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`,
		knowledgeBaseID, documentID, position, kind,
	).Scan(
		&result.DocumentID,
		&result.OriginalFilename,
		&result.Position,
		&result.Content,
		&chunkKind,
		&headingPath,
		&parentContent,
		&parentPosition,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SearchResult{}, ErrChunkNotFound
	}
	if err != nil {
		return SearchResult{}, fmt.Errorf("read document chunk: %w", err)
	}
	if parentContent.Valid {
		result.ParentContent = parentContent.String
	}
	if parentPosition.Valid {
		result.ParentPosition = int(parentPosition.Int64)
	}
	result.HeadingPath = headingPath
	if chunkKind != "text" {
		result.ChunkKind = chunkKind
	}
	return result, nil
}

// ReadRange returns a small, ordered window of chunks without loading an
// entire document. The extra SQL row tells callers whether more chunks exist;
// maxBytes protects both a preview response and a model tool result.
func (s *PostgresStore) ReadRange(ctx context.Context, knowledgeBaseID, documentID int64, startPosition, limit, maxBytes int) (RangeResult, error) {
	if knowledgeBaseID <= 0 || documentID <= 0 || startPosition < 0 || limit < 1 || maxBytes < 2 {
		return RangeResult{}, ErrChunkNotFound
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT chunks.document_id, documents.original_filename, chunks.position,
		       chunks.content, chunks.heading_path
		FROM document_chunks AS chunks
		JOIN documents ON documents.id = chunks.document_id
		JOIN knowledge_bases AS kb ON kb.id = documents.knowledge_base_id
		WHERE documents.knowledge_base_id = $1
		  AND documents.id = $2
		  AND chunks.position >= $3
		  AND chunks.chunk_kind = 'text'
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		ORDER BY chunks.position
		LIMIT $4`, knowledgeBaseID, documentID, startPosition, limit+1)
	if err != nil {
		return RangeResult{}, fmt.Errorf("read document chunks: %w", err)
	}
	defer rows.Close()

	result := RangeResult{Chunks: make([]SearchResult, 0, limit), NextPosition: startPosition}
	bytesUsed := 0
	for rows.Next() {
		var chunk SearchResult
		if err := rows.Scan(&chunk.DocumentID, &chunk.OriginalFilename, &chunk.Position, &chunk.Content, &chunk.HeadingPath); err != nil {
			return RangeResult{}, fmt.Errorf("scan document chunk range: %w", err)
		}
		if len(result.Chunks) >= limit {
			result.Truncated = true
			break
		}
		remaining := maxBytes - bytesUsed
		if remaining <= 0 {
			result.Truncated = true
			break
		}
		if len(chunk.Content) > remaining {
			chunk.Content = truncateUTF8(chunk.Content, remaining)
			result.Truncated = true
		}
		result.Chunks = append(result.Chunks, chunk)
		bytesUsed += len(chunk.Content)
		result.NextPosition = chunk.Position + 1
		if result.Truncated {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return RangeResult{}, fmt.Errorf("iterate document chunk range: %w", err)
	}
	if len(result.Chunks) == 0 {
		return RangeResult{}, ErrChunkNotFound
	}
	return result, nil
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	truncated := value[:maxBytes]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}

func (s *PostgresStore) Replace(ctx context.Context, documentID int64, chunks []string, embeddings [][]float32) error {
	if len(embeddings) != 0 && len(embeddings) != len(chunks) {
		return fmt.Errorf("embedding count = %d, want %d", len(embeddings), len(chunks))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin chunk transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM document_chunks WHERE document_id = $1`, documentID); err != nil {
		return fmt.Errorf("delete document chunks: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM document_parent_chunks WHERE document_id = $1`, documentID); err != nil {
		return fmt.Errorf("delete document parent chunks: %w", err)
	}
	for position, content := range chunks {
		var embedding any
		if len(embeddings) != 0 {
			embedding = vectorLiteral(embeddings[position])
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO document_chunks (document_id, position, chunk_kind, content, heading_path, embedding) VALUES ($1, $2, 'text', $3, '', $4::vector)`, documentID, position, content, embedding); err != nil {
			return fmt.Errorf("create document chunk: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit chunk transaction: %w", err)
	}
	return nil
}

// ReplaceHierarchical stores parent chunks without embeddings and child chunks
// with embeddings. Parent positions are local to one document; child positions
// remain the citation positions exposed by the existing search API.
func (s *PostgresStore) ReplaceHierarchical(ctx context.Context, documentID int64, parents []ParentChunk, children []ChildChunk, embeddings [][]float32) error {
	if documentID <= 0 || len(parents) == 0 || len(children) == 0 || len(embeddings) != len(children) {
		return fmt.Errorf("invalid hierarchical chunk data")
	}
	parentByPosition := make(map[int]int64, len(parents))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin hierarchical chunk transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM document_chunks WHERE document_id = $1`, documentID); err != nil {
		return fmt.Errorf("delete document chunks: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM document_parent_chunks WHERE document_id = $1`, documentID); err != nil {
		return fmt.Errorf("delete document parent chunks: %w", err)
	}
	for _, parent := range parents {
		var parentID int64
		err := tx.QueryRowContext(ctx, `INSERT INTO document_parent_chunks (document_id, position, content, heading_path) VALUES ($1, $2, $3, $4) RETURNING id`, documentID, parent.Position, parent.Content, parent.HeadingPath).Scan(&parentID)
		if err != nil {
			return fmt.Errorf("create document parent chunk: %w", err)
		}
		parentByPosition[parent.Position] = parentID
	}
	for index, child := range children {
		parentID, ok := parentByPosition[child.ParentPosition]
		if !ok {
			return fmt.Errorf("child position %d references unknown parent position %d", child.Position, child.ParentPosition)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO document_chunks (document_id, position, chunk_kind, parent_chunk_id, content, heading_path, embedding) VALUES ($1, $2, 'text', $3, $4, $5, $6::vector)`, documentID, child.Position, parentID, child.Content, child.HeadingPath, vectorLiteral(embeddings[index])); err != nil {
			return fmt.Errorf("create document child chunk: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit hierarchical chunk transaction: %w", err)
	}
	return nil
}

// ReplaceSummary stores one generated document summary as a separate
// searchable chunk. Position zero is safe because chunk_kind participates in
// the uniqueness constraint and document readers filter to text chunks.
func (s *PostgresStore) ReplaceSummary(ctx context.Context, documentID int64, content string, embedding []float32) error {
	if documentID <= 0 || strings.TrimSpace(content) == "" || len(embedding) == 0 {
		return errors.New("invalid document summary chunk")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin summary chunk transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM document_chunks WHERE document_id = $1 AND chunk_kind = 'summary'`, documentID); err != nil {
		return fmt.Errorf("delete document summary chunk: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO document_chunks (document_id, position, chunk_kind, content, heading_path, embedding) VALUES ($1, 0, 'summary', $2, '', $3::vector)`, documentID, content, vectorLiteral(embedding)); err != nil {
		return fmt.Errorf("create document summary chunk: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit document summary chunk: %w", err)
	}
	return nil
}

// ParentForChunk returns the parent context for a child. A missing parent is
// not an error so legacy chunks can continue through neighbor expansion.
func (s *PostgresStore) ParentForChunk(ctx context.Context, knowledgeBaseID, documentID int64, position int) (ParentChunk, bool, error) {
	var parent ParentChunk
	err := s.db.QueryRowContext(ctx, `SELECT parents.position, parents.content, parents.heading_path
		FROM document_chunks AS chunks
		JOIN document_parent_chunks AS parents ON parents.id = chunks.parent_chunk_id
		JOIN documents AS documents ON documents.id = chunks.document_id
		WHERE documents.knowledge_base_id = $1 AND chunks.document_id = $2 AND chunks.position = $3 AND chunks.chunk_kind = 'text'`, knowledgeBaseID, documentID, position).Scan(&parent.Position, &parent.Content, &parent.HeadingPath)
	if errors.Is(err, sql.ErrNoRows) {
		return ParentChunk{}, false, nil
	}
	if err != nil {
		return ParentChunk{}, false, fmt.Errorf("query parent chunk: %w", err)
	}
	return parent, true, nil
}

// ParentsForChunks loads all parents for the requested children in one query.
// Missing rows are omitted so callers can keep their legacy fallback path.
func (s *PostgresStore) ParentsForChunks(ctx context.Context, knowledgeBaseID int64, references []ChunkReference) (map[ChunkReference]ParentChunk, error) {
	parents := make(map[ChunkReference]ParentChunk, len(references))
	if len(references) == 0 {
		return parents, nil
	}
	documentIDs := make([]int64, len(references))
	positions := make([]int64, len(references))
	for index, reference := range references {
		documentIDs[index] = reference.DocumentID
		positions[index] = int64(reference.Position)
	}
	rows, err := s.db.QueryContext(ctx, `
		WITH requested AS (
			SELECT * FROM unnest($2::bigint[], $3::bigint[]) AS requested(document_id, child_position)
		)
		SELECT chunks.document_id, chunks.position, parents.position, parents.content, parents.heading_path
		FROM requested
		JOIN document_chunks AS chunks
		  ON chunks.document_id = requested.document_id
		 AND chunks.position = requested.child_position::integer
		 AND chunks.chunk_kind = 'text'
		JOIN document_parent_chunks AS parents ON parents.id = chunks.parent_chunk_id
		JOIN documents ON documents.id = chunks.document_id
		WHERE documents.knowledge_base_id = $1`, knowledgeBaseID, documentIDs, positions)
	if err != nil {
		return nil, fmt.Errorf("query parent chunks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var reference ChunkReference
		var parent ParentChunk
		if err := rows.Scan(&reference.DocumentID, &reference.Position, &parent.Position, &parent.Content, &parent.HeadingPath); err != nil {
			return nil, fmt.Errorf("scan parent chunk: %w", err)
		}
		parents[reference] = parent
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate parent chunks: %w", err)
	}
	return parents, nil
}

func (s *PostgresStore) Search(ctx context.Context, knowledgeBaseID int64, embedding []float32, limit int) ([]SearchResult, error) {
	return s.SearchWithDocuments(ctx, knowledgeBaseID, embedding, limit, nil)
}

func (s *PostgresStore) SearchWithDocuments(ctx context.Context, knowledgeBaseID int64, embedding []float32, limit int, documentIDs []int64) ([]SearchResult, error) {
	return s.searchVectorWithScope(ctx, knowledgeBaseID, embedding, limit, documentIDs, nil, false, nil)
}

func (s *PostgresStore) SearchWithFolder(ctx context.Context, knowledgeBaseID int64, embedding []float32, limit int, documentIDs []int64, folderPath string, recursive bool) ([]SearchResult, error) {
	return s.searchVectorWithScope(ctx, knowledgeBaseID, embedding, limit, documentIDs, folderPath, recursive, nil)
}

func (s *PostgresStore) SearchWithTags(ctx context.Context, knowledgeBaseID int64, embedding []float32, limit int, documentIDs []int64, folderPath string, recursive bool, tagIDs []int64) ([]SearchResult, error) {
	var folderScope any
	if folderPath != "" {
		folderScope = folderPath
	}
	return s.searchVectorWithScope(ctx, knowledgeBaseID, embedding, limit, documentIDs, folderScope, recursive, tagIDs)
}

func (s *PostgresStore) searchVectorWithScope(ctx context.Context, knowledgeBaseID int64, embedding []float32, limit int, documentIDs []int64, folderPath any, recursive bool, tagIDs []int64) ([]SearchResult, error) {
	queryVector := vectorLiteral(embedding)
	query := "SELECT chunks.document_id, documents.original_filename, chunks.position, chunks.content, chunks.chunk_kind, chunks.heading_path, " +
		"chunks.embedding <=> $2::vector AS distance " +
		"FROM document_chunks AS chunks JOIN documents AS documents ON documents.id = chunks.document_id " +
		"WHERE documents.knowledge_base_id = $1 AND chunks.embedding IS NOT NULL " +
		"AND ($4::bigint[] IS NULL OR chunks.document_id = ANY($4::bigint[])) " +
		"AND ($5::text IS NULL OR documents.folder_path = $5 OR ($6::boolean AND LEFT(documents.folder_path, LENGTH($5) + 1) = $5 || '/')) " +
		"AND ($7::bigint[] IS NULL OR EXISTS (SELECT 1 FROM document_tags AS link JOIN knowledge_base_tags AS tag ON tag.id = link.tag_id WHERE link.document_id = chunks.document_id AND tag.knowledge_base_id = documents.knowledge_base_id AND tag.id = ANY($7::bigint[]))) " +
		"ORDER BY chunks.embedding <=> $2::vector, chunks.position LIMIT $3"
	rows, err := s.db.QueryContext(ctx, query, knowledgeBaseID, queryVector, limit, documentIDsArgument(documentIDs), folderPath, recursive, documentIDsArgument(tagIDs))
	if err != nil {
		return nil, fmt.Errorf("query filtered document chunks: %w", err)
	}
	defer rows.Close()

	results := make([]SearchResult, 0, limit)
	for rows.Next() {
		var result SearchResult
		var chunkKind string
		if err := rows.Scan(&result.DocumentID, &result.OriginalFilename, &result.Position, &result.Content, &chunkKind, &result.HeadingPath, &result.Distance); err != nil {
			return nil, fmt.Errorf("scan filtered document chunk: %w", err)
		}
		if chunkKind != "text" {
			result.ChunkKind = chunkKind
		}
		result.MatchType = "vector"
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate filtered document chunks: %w", err)
	}
	return results, nil
}

// SearchKeyword returns chunks that contain the query text. It is a small
// lexical companion to vector search for exact names, codes and commands.
func (s *PostgresStore) SearchKeyword(ctx context.Context, knowledgeBaseID int64, query string, limit int) ([]SearchResult, error) {
	return s.SearchKeywordWithDocuments(ctx, knowledgeBaseID, query, limit, nil)
}

func (s *PostgresStore) SearchKeywordWithDocuments(ctx context.Context, knowledgeBaseID int64, query string, limit int, documentIDs []int64) ([]SearchResult, error) {
	return s.searchKeywordWithScope(ctx, knowledgeBaseID, query, limit, documentIDs, nil, false)
}

func (s *PostgresStore) SearchKeywordWithFolder(ctx context.Context, knowledgeBaseID int64, query string, limit int, documentIDs []int64, folderPath string, recursive bool) ([]SearchResult, error) {
	return s.searchKeywordWithScope(ctx, knowledgeBaseID, query, limit, documentIDs, folderPath, recursive)
}

func (s *PostgresStore) SearchKeywordWithTags(ctx context.Context, knowledgeBaseID int64, query string, limit int, documentIDs []int64, folderPath string, recursive bool, tagIDs []int64) ([]SearchResult, error) {
	var folderScope any
	if folderPath != "" {
		folderScope = folderPath
	}
	return s.searchKeywordWithTagScope(ctx, knowledgeBaseID, query, limit, documentIDs, folderScope, recursive, tagIDs)
}

func (s *PostgresStore) searchKeywordWithScope(ctx context.Context, knowledgeBaseID int64, query string, limit int, documentIDs []int64, folderPath any, recursive bool) ([]SearchResult, error) {
	return s.searchKeywordWithTagScope(ctx, knowledgeBaseID, query, limit, documentIDs, folderPath, recursive, nil)
}

func (s *PostgresStore) searchKeywordWithTagScope(ctx context.Context, knowledgeBaseID int64, query string, limit int, documentIDs []int64, folderPath any, recursive bool, tagIDs []int64) ([]SearchResult, error) {
	exactPattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(strings.ToLower(query)) + "%"
	sqlQuery := "WITH search_query AS (" +
		"SELECT plainto_tsquery('simple', $2) AS terms" +
		"), scored AS (" +
		"SELECT chunks.document_id, documents.original_filename, chunks.position, chunks.content, chunks.chunk_kind, chunks.heading_path, " +
		"ts_rank_cd(chunks.content_search, search_query.terms) + 0.35 * ts_rank_cd(chunks.heading_search, search_query.terms) AS keyword_score, " +
		"ts_rank_cd(chunks.heading_search, search_query.terms) AS heading_score, " +
		"CASE WHEN lower(chunks.content) LIKE $3 ESCAPE E'\\\\' OR lower(chunks.heading_path) LIKE $3 ESCAPE E'\\\\' OR lower(documents.original_filename) LIKE $3 ESCAPE E'\\\\' THEN 1.0 ELSE 0.0 END AS exact_score " +
		"FROM document_chunks AS chunks JOIN documents AS documents ON documents.id = chunks.document_id " +
		"CROSS JOIN search_query WHERE documents.knowledge_base_id = $1 " +
		"AND (chunks.content_search @@ search_query.terms OR chunks.heading_search @@ search_query.terms OR lower(chunks.content) LIKE $3 ESCAPE E'\\\\' OR lower(chunks.heading_path) LIKE $3 ESCAPE E'\\\\' OR lower(documents.original_filename) LIKE $3 ESCAPE E'\\\\') " +
		"AND ($4::bigint[] IS NULL OR chunks.document_id = ANY($4::bigint[])) " +
		"AND ($5::text IS NULL OR documents.folder_path = $5 OR ($6::boolean AND LEFT(documents.folder_path, LENGTH($5) + 1) = $5 || '/')) " +
		"AND ($7::bigint[] IS NULL OR EXISTS (SELECT 1 FROM document_tags AS link JOIN knowledge_base_tags AS tag ON tag.id = link.tag_id WHERE link.document_id = chunks.document_id AND tag.knowledge_base_id = documents.knowledge_base_id AND tag.id = ANY($7::bigint[])))" +
		") SELECT document_id, original_filename, position, content, chunk_kind, heading_path, 0::float8 AS distance, GREATEST(keyword_score, exact_score) AS keyword_score, heading_score " +
		"FROM scored ORDER BY exact_score DESC, keyword_score DESC, position, document_id LIMIT $8"
	rows, err := s.db.QueryContext(ctx, sqlQuery, knowledgeBaseID, query, exactPattern, documentIDsArgument(documentIDs), folderPath, recursive, documentIDsArgument(tagIDs), limit)
	if err != nil {
		return nil, fmt.Errorf("query filtered keyword document chunks: %w", err)
	}
	defer rows.Close()

	results := make([]SearchResult, 0, limit)
	for rows.Next() {
		var result SearchResult
		var chunkKind string
		if err := rows.Scan(&result.DocumentID, &result.OriginalFilename, &result.Position, &result.Content, &chunkKind, &result.HeadingPath, &result.Distance, &result.KeywordScore, &result.HeadingScore); err != nil {
			return nil, fmt.Errorf("scan filtered keyword document chunk: %w", err)
		}
		if chunkKind != "text" {
			result.ChunkKind = chunkKind
		}
		result.MatchType = "keyword"
		result.KeywordScoreKnown = true
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate filtered keyword document chunks: %w", err)
	}
	return results, nil
}

// SearchNeighbors returns nearby chunks from the same document and knowledge
// base. The knowledge-base condition prevents context expansion from crossing
// the retrieval permission boundary.
func (s *PostgresStore) SearchNeighbors(ctx context.Context, knowledgeBaseID, documentID int64, position, before, after int) ([]SearchResult, error) {
	if knowledgeBaseID <= 0 || documentID <= 0 || position < 0 || before < 0 || after < 0 {
		return nil, errors.New("invalid chunk context lookup")
	}
	query := `SELECT chunks.position, chunks.content
		FROM document_chunks AS chunks
		JOIN documents AS documents ON documents.id = chunks.document_id
		WHERE documents.knowledge_base_id = $1
		  AND chunks.document_id = $2
		  AND chunks.position BETWEEN $3 AND $4
		  AND chunks.chunk_kind = 'text'
		ORDER BY chunks.position`
	rows, err := s.db.QueryContext(ctx, query, knowledgeBaseID, documentID, position-before, position+after)
	if err != nil {
		return nil, fmt.Errorf("query neighboring document chunks: %w", err)
	}
	defer rows.Close()

	results := make([]SearchResult, 0, before+after+1)
	for rows.Next() {
		var result SearchResult
		if err := rows.Scan(&result.Position, &result.Content); err != nil {
			return nil, fmt.Errorf("scan neighboring document chunk: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate neighboring document chunks: %w", err)
	}
	return results, nil
}

func documentIDsArgument(documentIDs []int64) any {
	if len(documentIDs) == 0 {
		return nil
	}
	parts := make([]string, len(documentIDs))
	for index, documentID := range documentIDs {
		parts[index] = strconv.FormatInt(documentID, 10)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func vectorLiteral(vector []float32) string {
	values := make([]string, len(vector))
	for index, value := range vector {
		values[index] = strconv.FormatFloat(float64(value), 'g', -1, 32)
	}
	return "[" + strings.Join(values, ",") + "]"
}
