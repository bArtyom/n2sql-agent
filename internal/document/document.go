package document

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/documentsummary"
	"github.com/jackc/pgx/v5/pgconn"
)

const MaxFileBytes int64 = 10 << 20

var (
	ErrKnowledgeBaseNotFound = errors.New("knowledge base not found")
	ErrDocumentNotFound      = errors.New("document not found")
	ErrDocumentProcessing    = errors.New("document is still processing")
	ErrDeleteUnavailable     = errors.New("document deletion is unavailable")
	ErrUnsupportedFile       = errors.New("unsupported file")
	ErrFileTooLarge          = errors.New("file is too large")
	ErrDuplicateDocument     = errors.New("document already exists")
	ErrInvalidStoragePath    = errors.New("invalid document storage path")
)

type Document struct {
	ID                  int64                          `json:"id"`
	KnowledgeBaseID     int64                          `json:"knowledgeBaseId"`
	OriginalFilename    string                         `json:"originalFilename"`
	ContentType         string                         `json:"contentType"`
	SizeBytes           int64                          `json:"sizeBytes"`
	ContentSHA256       string                         `json:"contentSha256,omitempty"`
	ProcessingStatus    string                         `json:"processingStatus"`
	SummaryStatus       string                         `json:"summaryStatus,omitempty"`
	SummaryIndexStatus  string                         `json:"summaryIndexStatus,omitempty"`
	ChunkingDiagnostics documentchunk.SplitDiagnostics `json:"chunkingDiagnostics"`
}

// Summary is the cached, document-level summary generated on demand.
type Summary = documentsummary.Summary

type UploadInput struct {
	KnowledgeBaseID  int64
	OriginalFilename string
	ContentType      string
	Content          io.Reader
}

type Uploader interface {
	Upload(context.Context, UploadInput) (Document, error)
}

// Deleter removes a document and its derived database records.
type Deleter interface {
	Delete(context.Context, int64, int64) error
}

type Reprocessor interface {
	Reprocess(context.Context, int64, int64) error
}

type CreateInput struct {
	KnowledgeBaseID  int64
	OriginalFilename string
	StoragePath      string
	ContentType      string
	SizeBytes        int64
	ContentSHA256    string
}

type Store interface {
	EnsureKnowledgeBase(context.Context, int64) error
	List(context.Context, int64) ([]Document, error)
	Create(context.Context, CreateInput) (Document, error)
}

// DeleteStore is optional so existing document store implementations remain
// compatible with the upload/list interfaces.
type DeleteStore interface {
	Delete(context.Context, int64, int64) (string, error)
}

// CacheInvalidator is implemented by retrieval services that cache results
// per knowledge base. Document deletion must invalidate those results.
type CacheInvalidator interface {
	ClearCache(int64)
}

type Reader interface {
	List(context.Context, int64) ([]Document, error)
}

type FileStore interface {
	Save(context.Context, string, io.Reader) (string, int64, string, error)
	Delete(context.Context, string) error
}

type Asset struct {
	OriginalFilename string
	ContentType      string
	SizeBytes        int64
	Content          io.ReadSeeker
	Close            func() error
}

type AssetReader interface {
	OpenAsset(context.Context, int64, int64) (Asset, error)
}

type assetMetadataStore interface {
	AssetMetadata(context.Context, int64, int64) (string, string, string, int64, error)
}

type assetFileOpener interface {
	Open(context.Context, string) (io.ReadSeeker, func() error, error)
}

type Service struct {
	store       Store
	files       FileStore
	invalidator CacheInvalidator
}

func NewService(store Store, files FileStore) *Service { return &Service{store: store, files: files} }

func NewServiceWithInvalidator(store Store, files FileStore, invalidator CacheInvalidator) *Service {
	return &Service{store: store, files: files, invalidator: invalidator}
}

func (s *Service) Upload(ctx context.Context, input UploadInput) (Document, error) {
	if input.KnowledgeBaseID <= 0 {
		return Document{}, ErrKnowledgeBaseNotFound
	}
	extension, ok := extensionForContentType(input.ContentType)
	if !ok || input.Content == nil || input.OriginalFilename == "" {
		return Document{}, ErrUnsupportedFile
	}
	if err := s.store.EnsureKnowledgeBase(ctx, input.KnowledgeBaseID); err != nil {
		return Document{}, err
	}
	storagePath, sizeBytes, contentSHA256, err := s.files.Save(ctx, extension, io.LimitReader(input.Content, MaxFileBytes+1))
	if err != nil {
		return Document{}, err
	}
	if sizeBytes > MaxFileBytes {
		_ = s.files.Delete(context.WithoutCancel(ctx), storagePath)
		return Document{}, ErrFileTooLarge
	}
	document, err := s.store.Create(ctx, CreateInput{
		KnowledgeBaseID:  input.KnowledgeBaseID,
		OriginalFilename: input.OriginalFilename,
		StoragePath:      storagePath,
		ContentType:      input.ContentType,
		SizeBytes:        sizeBytes,
		ContentSHA256:    contentSHA256,
	})
	if err != nil {
		_ = s.files.Delete(context.WithoutCancel(ctx), storagePath)
		return Document{}, err
	}
	return document, nil
}

func (s *Service) OpenAsset(ctx context.Context, knowledgeBaseID, documentID int64) (Asset, error) {
	metadataStore, ok := s.store.(assetMetadataStore)
	if !ok {
		return Asset{}, ErrUnsupportedFile
	}
	opener, ok := s.files.(assetFileOpener)
	if !ok {
		return Asset{}, ErrUnsupportedFile
	}
	storagePath, filename, contentType, sizeBytes, err := metadataStore.AssetMetadata(ctx, knowledgeBaseID, documentID)
	if err != nil {
		return Asset{}, err
	}
	content, closeFile, err := opener.Open(ctx, storagePath)
	if err != nil {
		return Asset{}, err
	}
	return Asset{OriginalFilename: filename, ContentType: contentType, SizeBytes: sizeBytes, Content: content, Close: closeFile}, nil
}

func (s *Service) List(ctx context.Context, knowledgeBaseID int64) ([]Document, error) {
	if knowledgeBaseID <= 0 {
		return nil, ErrKnowledgeBaseNotFound
	}
	if err := s.store.EnsureKnowledgeBase(ctx, knowledgeBaseID); err != nil {
		return nil, err
	}
	documents, err := s.store.List(ctx, knowledgeBaseID)
	if err != nil {
		return nil, err
	}
	return documents, nil
}

func (s *Service) Reprocess(ctx context.Context, knowledgeBaseID, documentID int64) error {
	if knowledgeBaseID <= 0 || documentID <= 0 {
		return ErrDocumentNotFound
	}
	reprocessor, ok := s.store.(Reprocessor)
	if !ok {
		return ErrDeleteUnavailable
	}
	return reprocessor.Reprocess(ctx, knowledgeBaseID, documentID)
}

// Delete removes the database record first so PostgreSQL can cascade chunks,
// parent chunks, and processing tasks atomically. Local file cleanup is then
// best effort: a failed cleanup is logged, while the document remains deleted
// from the application and retrieval cache.
func (s *Service) Delete(ctx context.Context, knowledgeBaseID, documentID int64) error {
	if knowledgeBaseID <= 0 || documentID <= 0 {
		return ErrDocumentNotFound
	}
	deleteStore, ok := s.store.(DeleteStore)
	if !ok {
		return ErrDeleteUnavailable
	}
	storagePath, err := deleteStore.Delete(ctx, knowledgeBaseID, documentID)
	if err != nil {
		return err
	}
	if s.invalidator != nil {
		s.invalidator.ClearCache(knowledgeBaseID)
	}
	if err := s.files.Delete(context.WithoutCancel(ctx), storagePath); err != nil {
		slog.ErrorContext(ctx, "document_file_cleanup_failed", "document_id", documentID, "knowledge_base_id", knowledgeBaseID, "error", err)
	}
	return nil
}

type LocalFileStore struct{ root string }

func NewLocalFileStore(root string) *LocalFileStore { return &LocalFileStore{root: root} }

func (s *LocalFileStore) Save(ctx context.Context, extension string, content io.Reader) (string, int64, string, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, "", err
	}
	if extension != ".md" && extension != ".txt" && extension != ".html" && extension != ".pdf" && extension != ".docx" && extension != ".pptx" && extension != ".xlsx" && extension != ".png" && extension != ".jpg" && extension != ".webp" {
		return "", 0, "", ErrUnsupportedFile
	}
	directory := filepath.Join(s.root, "documents")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return "", 0, "", fmt.Errorf("create document directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".upload-*")
	if err != nil {
		return "", 0, "", fmt.Errorf("create temporary document: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	hasher := sha256.New()
	sizeBytes, copyErr := io.Copy(io.MultiWriter(temporary, hasher), content)
	closeErr := temporary.Close()
	if copyErr != nil {
		return "", 0, "", fmt.Errorf("write document: %w", copyErr)
	}
	if closeErr != nil {
		return "", 0, "", fmt.Errorf("close document: %w", closeErr)
	}
	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return "", 0, "", fmt.Errorf("generate document path: %w", err)
	}
	relativePath := filepath.Join("documents", hex.EncodeToString(identifier)+extension)
	if err := os.Rename(temporaryPath, filepath.Join(s.root, relativePath)); err != nil {
		return "", 0, "", fmt.Errorf("finalize document: %w", err)
	}
	return relativePath, sizeBytes, hex.EncodeToString(hasher.Sum(nil)), nil
}

func (s *LocalFileStore) Open(ctx context.Context, storagePath string) (io.ReadSeeker, func() error, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	storagePath = filepath.FromSlash(storagePath)
	if filepath.IsAbs(storagePath) || filepath.Clean(storagePath) != storagePath || filepath.Dir(storagePath) != "documents" {
		return nil, nil, ErrInvalidStoragePath
	}
	file, err := os.Open(filepath.Join(s.root, storagePath))
	if err != nil {
		return nil, nil, fmt.Errorf("open document asset: %w", err)
	}
	return file, file.Close, nil
}

func (s *LocalFileStore) Delete(ctx context.Context, storagePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Database paths use forward slashes on every platform. Normalize before
	// validating so cleanup also works on Windows.
	storagePath = filepath.FromSlash(storagePath)
	if filepath.IsAbs(storagePath) || filepath.Clean(storagePath) != storagePath || filepath.Dir(storagePath) != "documents" {
		return errors.New("invalid document storage path")
	}
	if err := os.Remove(filepath.Join(s.root, storagePath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove document: %w", err)
	}
	return nil
}

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) EnsureKnowledgeBase(ctx context.Context, id int64) error {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM knowledge_bases
		WHERE id = $1 AND administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
	)`, id).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check knowledge base: %w", err)
	}
	if !exists {
		return ErrKnowledgeBaseNotFound
	}
	return nil
}

func (s *PostgresStore) AssetMetadata(ctx context.Context, knowledgeBaseID, documentID int64) (string, string, string, int64, error) {
	var storagePath, filename, contentType string
	var sizeBytes int64
	err := s.db.QueryRowContext(ctx, `
		SELECT d.storage_path, d.original_filename, d.content_type, d.size_bytes
		FROM documents AS d
		JOIN knowledge_bases AS kb ON kb.id = d.knowledge_base_id
		WHERE d.id = $1 AND d.knowledge_base_id = $2
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, documentID, knowledgeBaseID).
		Scan(&storagePath, &filename, &contentType, &sizeBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", 0, ErrDocumentNotFound
	}
	if err != nil {
		return "", "", "", 0, fmt.Errorf("read document asset metadata: %w", err)
	}
	if !strings.HasPrefix(contentType, "image/") {
		return "", "", "", 0, ErrUnsupportedFile
	}
	return storagePath, filename, contentType, sizeBytes, nil
}

func (s *PostgresStore) List(ctx context.Context, knowledgeBaseID int64) ([]Document, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.id, d.knowledge_base_id, d.original_filename, d.content_type, d.size_bytes, d.content_sha256,
		       d.chunking_diagnostics, d.summary_status, d.summary_index_status,
		       COALESCE(task.status, 'pending') AS processing_status
		FROM documents AS d
		LEFT JOIN LATERAL (
			SELECT status
			FROM document_processing_tasks
			WHERE document_id = d.id
			ORDER BY created_at DESC, id DESC
			LIMIT 1
		) AS task ON TRUE
		WHERE d.knowledge_base_id = $1
		  AND d.knowledge_base_id IN (
			SELECT id FROM knowledge_bases
			WHERE administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		  )
		ORDER BY d.created_at DESC, d.id DESC`, knowledgeBaseID)
	if err != nil {
		return nil, fmt.Errorf("list documents: %w", err)
	}
	defer rows.Close()

	documents := make([]Document, 0)
	for rows.Next() {
		var document Document
		var diagnostics []byte
		if err := rows.Scan(
			&document.ID,
			&document.KnowledgeBaseID,
			&document.OriginalFilename,
			&document.ContentType,
			&document.SizeBytes,
			&document.ContentSHA256,
			&diagnostics,
			&document.SummaryStatus,
			&document.SummaryIndexStatus,
			&document.ProcessingStatus,
		); err != nil {
			return nil, fmt.Errorf("scan document: %w", err)
		}
		if len(diagnostics) > 0 {
			if err := json.Unmarshal(diagnostics, &document.ChunkingDiagnostics); err != nil {
				return nil, fmt.Errorf("decode chunking diagnostics: %w", err)
			}
		}
		documents = append(documents, document)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate documents: %w", err)
	}
	return documents, nil
}

func (s *PostgresStore) Reprocess(ctx context.Context, knowledgeBaseID, documentID int64) error {
	var exists, active bool
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM documents AS d
			JOIN knowledge_bases AS kb ON kb.id = d.knowledge_base_id
			WHERE d.id = $1 AND d.knowledge_base_id = $2
			  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		), EXISTS (
			SELECT 1 FROM document_processing_tasks
			WHERE document_id = $1 AND status IN ('pending', 'processing')
		)`, documentID, knowledgeBaseID).Scan(&exists, &active); err != nil {
		return fmt.Errorf("check document reprocess status: %w", err)
	}
	if !exists {
		return ErrDocumentNotFound
	}
	if active {
		return ErrDocumentProcessing
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO document_processing_tasks (document_id) VALUES ($1)`, documentID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.ConstraintName == "document_processing_tasks_active_document_idx" {
			return ErrDocumentProcessing
		}
		return fmt.Errorf("create document reprocess task: %w", err)
	}
	return nil
}

func (s *PostgresStore) GetSummary(ctx context.Context, knowledgeBaseID, documentID int64) (Summary, error) {
	var summary Summary
	err := s.db.QueryRowContext(ctx, `
		SELECT d.summary, d.summary_status, COALESCE(d.summary_error, ''), d.summary_updated_at,
		       d.summary_index_status, COALESCE(d.summary_index_error, ''), d.summary_index_updated_at
		FROM documents AS d
		JOIN knowledge_bases AS kb ON kb.id = d.knowledge_base_id
		WHERE d.id = $1 AND d.knowledge_base_id = $2
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, documentID, knowledgeBaseID).
		Scan(&summary.Content, &summary.Status, &summary.Error, &summary.UpdatedAt, &summary.IndexStatus, &summary.IndexError, &summary.IndexUpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Summary{}, ErrDocumentNotFound
	}
	if err != nil {
		return Summary{}, fmt.Errorf("get document summary: %w", err)
	}
	return summary, nil
}

func (s *PostgresStore) MarkSummaryProcessing(ctx context.Context, knowledgeBaseID, documentID int64) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE documents AS d SET summary_status = 'processing', summary_error = NULL
		FROM knowledge_bases AS kb
		WHERE d.knowledge_base_id = kb.id AND d.id = $1 AND d.knowledge_base_id = $2
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, documentID, knowledgeBaseID)
	if err != nil {
		return fmt.Errorf("mark document summary processing: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrDocumentNotFound
	}
	return nil
}

func (s *PostgresStore) SaveSummary(ctx context.Context, knowledgeBaseID, documentID int64, content string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE documents AS d SET summary = $3, summary_status = 'succeeded', summary_error = NULL, summary_updated_at = CURRENT_TIMESTAMP,
			summary_index_status = 'none', summary_index_error = NULL, summary_index_updated_at = NULL
		FROM knowledge_bases AS kb
		WHERE d.knowledge_base_id = kb.id AND d.id = $1 AND d.knowledge_base_id = $2
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, documentID, knowledgeBaseID, content)
	if err != nil {
		return fmt.Errorf("save document summary: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrDocumentNotFound
	}
	return nil
}

func (s *PostgresStore) SaveSummaryError(ctx context.Context, knowledgeBaseID, documentID int64, message string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE documents AS d SET summary_status = 'failed', summary_error = $3, summary_updated_at = CURRENT_TIMESTAMP
		FROM knowledge_bases AS kb
		WHERE d.knowledge_base_id = kb.id AND d.id = $1 AND d.knowledge_base_id = $2
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, documentID, knowledgeBaseID, message)
	if err != nil {
		return fmt.Errorf("save document summary error: %w", err)
	}
	return nil
}

func (s *PostgresStore) MarkSummaryIndexProcessing(ctx context.Context, knowledgeBaseID, documentID int64) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE documents AS d SET summary_index_status = 'processing', summary_index_error = NULL
		FROM knowledge_bases AS kb
		WHERE d.knowledge_base_id = kb.id AND d.id = $1 AND d.knowledge_base_id = $2
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		  AND d.summary_status = 'succeeded'
		  AND d.summary_index_status NOT IN ('processing', 'succeeded')`, documentID, knowledgeBaseID)
	if err != nil {
		return false, fmt.Errorf("mark document summary index processing: %w", err)
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (s *PostgresStore) SaveSummaryIndexSuccess(ctx context.Context, knowledgeBaseID, documentID int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE documents AS d SET summary_index_status = 'succeeded', summary_index_error = NULL, summary_index_updated_at = CURRENT_TIMESTAMP
		FROM knowledge_bases AS kb
		WHERE d.knowledge_base_id = kb.id AND d.id = $1 AND d.knowledge_base_id = $2
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, documentID, knowledgeBaseID)
	if err != nil {
		return fmt.Errorf("save document summary index success: %w", err)
	}
	return nil
}

func (s *PostgresStore) SaveSummaryIndexError(ctx context.Context, knowledgeBaseID, documentID int64, message string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE documents AS d SET summary_index_status = 'failed', summary_index_error = $3, summary_index_updated_at = CURRENT_TIMESTAMP
		FROM knowledge_bases AS kb
		WHERE d.knowledge_base_id = kb.id AND d.id = $1 AND d.knowledge_base_id = $2
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, documentID, knowledgeBaseID, message)
	if err != nil {
		return fmt.Errorf("save document summary index error: %w", err)
	}
	return nil
}

func (s *PostgresStore) Create(ctx context.Context, input CreateInput) (Document, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Document{}, fmt.Errorf("begin document transaction: %w", err)
	}
	defer tx.Rollback()
	var document Document
	err = tx.QueryRowContext(ctx, `
		INSERT INTO documents (knowledge_base_id, original_filename, storage_path, content_type, size_bytes, content_sha256)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, knowledge_base_id, original_filename, content_type, size_bytes, content_sha256`,
		input.KnowledgeBaseID, input.OriginalFilename, input.StoragePath, input.ContentType, input.SizeBytes, input.ContentSHA256,
	).Scan(&document.ID, &document.KnowledgeBaseID, &document.OriginalFilename, &document.ContentType, &document.SizeBytes, &document.ContentSHA256)
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23503" {
			return Document{}, ErrKnowledgeBaseNotFound
		}
		if errors.As(err, &pgError) && pgError.ConstraintName == "documents_knowledge_base_content_sha256_idx" {
			return Document{}, ErrDuplicateDocument
		}
		return Document{}, fmt.Errorf("create document: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO document_processing_tasks (document_id) VALUES ($1)`, document.ID); err != nil {
		return Document{}, fmt.Errorf("create document processing task: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Document{}, fmt.Errorf("commit document transaction: %w", err)
	}
	document.ProcessingStatus = "pending"
	return document, nil
}

// Delete removes only a document owned by the current administrator and
// refuses to race an active worker task. The task row is locked before the
// status check, so a concurrent claim cannot change the decision underneath
// this transaction.
func (s *PostgresStore) Delete(ctx context.Context, knowledgeBaseID, documentID int64) (string, error) {
	if knowledgeBaseID <= 0 || documentID <= 0 {
		return "", ErrDocumentNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin document deletion: %w", err)
	}
	defer tx.Rollback()

	var storagePath string
	err = tx.QueryRowContext(ctx, `
		SELECT d.storage_path
		FROM documents AS d
		JOIN knowledge_bases AS kb ON kb.id = d.knowledge_base_id
		WHERE d.id = $1
		  AND d.knowledge_base_id = $2
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		FOR UPDATE`, documentID, knowledgeBaseID).Scan(&storagePath)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrDocumentNotFound
	}
	if err != nil {
		return "", fmt.Errorf("find document for deletion: %w", err)
	}

	var status string
	err = tx.QueryRowContext(ctx, `
		SELECT status
		FROM document_processing_tasks
		WHERE document_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT 1
		FOR UPDATE`, documentID).Scan(&status)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("check document processing status: %w", err)
	}
	if status == "pending" || status == "processing" {
		return "", ErrDocumentProcessing
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM documents WHERE id = $1`, documentID); err != nil {
		return "", fmt.Errorf("delete document: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit document deletion: %w", err)
	}
	return storagePath, nil
}

func extensionForContentType(contentType string) (string, bool) {
	switch contentType {
	case "text/markdown":
		return ".md", true
	case "text/plain":
		return ".txt", true
	case "text/html":
		return ".html", true
	case "application/pdf":
		return ".pdf", true
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return ".docx", true
	case "application/vnd.openxmlformats-officedocument.presentationml.presentation":
		return ".pptx", true
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return ".xlsx", true
	case "image/png":
		return ".png", true
	case "image/jpeg":
		return ".jpg", true
	case "image/webp":
		return ".webp", true
	default:
		return "", false
	}
}
