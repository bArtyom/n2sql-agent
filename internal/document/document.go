package document

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgconn"
)

const MaxFileBytes int64 = 10 << 20

var (
	ErrKnowledgeBaseNotFound = errors.New("knowledge base not found")
	ErrUnsupportedFile       = errors.New("unsupported file")
	ErrFileTooLarge          = errors.New("file is too large")
)

type Document struct {
	ID               int64  `json:"id"`
	KnowledgeBaseID  int64  `json:"knowledgeBaseId"`
	OriginalFilename string `json:"originalFilename"`
	ContentType      string `json:"contentType"`
	SizeBytes        int64  `json:"sizeBytes"`
	ProcessingStatus string `json:"processingStatus"`
}

type UploadInput struct {
	KnowledgeBaseID  int64
	OriginalFilename string
	ContentType      string
	Content          io.Reader
}

type Uploader interface {
	Upload(context.Context, UploadInput) (Document, error)
}

type CreateInput struct {
	KnowledgeBaseID  int64
	OriginalFilename string
	StoragePath      string
	ContentType      string
	SizeBytes        int64
}

type Store interface {
	EnsureKnowledgeBase(context.Context, int64) error
	List(context.Context, int64) ([]Document, error)
	Create(context.Context, CreateInput) (Document, error)
}

type Reader interface {
	List(context.Context, int64) ([]Document, error)
}

type FileStore interface {
	Save(context.Context, string, io.Reader) (string, int64, error)
	Delete(context.Context, string) error
}

type Service struct {
	store Store
	files FileStore
}

func NewService(store Store, files FileStore) *Service { return &Service{store: store, files: files} }

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
	storagePath, sizeBytes, err := s.files.Save(ctx, extension, io.LimitReader(input.Content, MaxFileBytes+1))
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
	})
	if err != nil {
		_ = s.files.Delete(context.WithoutCancel(ctx), storagePath)
		return Document{}, err
	}
	return document, nil
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

type LocalFileStore struct{ root string }

func NewLocalFileStore(root string) *LocalFileStore { return &LocalFileStore{root: root} }

func (s *LocalFileStore) Save(ctx context.Context, extension string, content io.Reader) (string, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	if extension != ".md" && extension != ".txt" && extension != ".pdf" {
		return "", 0, ErrUnsupportedFile
	}
	directory := filepath.Join(s.root, "documents")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return "", 0, fmt.Errorf("create document directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".upload-*")
	if err != nil {
		return "", 0, fmt.Errorf("create temporary document: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	sizeBytes, copyErr := io.Copy(temporary, content)
	closeErr := temporary.Close()
	if copyErr != nil {
		return "", 0, fmt.Errorf("write document: %w", copyErr)
	}
	if closeErr != nil {
		return "", 0, fmt.Errorf("close document: %w", closeErr)
	}
	identifier := make([]byte, 16)
	if _, err := rand.Read(identifier); err != nil {
		return "", 0, fmt.Errorf("generate document path: %w", err)
	}
	relativePath := filepath.Join("documents", hex.EncodeToString(identifier)+extension)
	if err := os.Rename(temporaryPath, filepath.Join(s.root, relativePath)); err != nil {
		return "", 0, fmt.Errorf("finalize document: %w", err)
	}
	return relativePath, sizeBytes, nil
}

func (s *LocalFileStore) Delete(ctx context.Context, storagePath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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

func (s *PostgresStore) List(ctx context.Context, knowledgeBaseID int64) ([]Document, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.id, d.knowledge_base_id, d.original_filename, d.content_type, d.size_bytes,
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
		if err := rows.Scan(
			&document.ID,
			&document.KnowledgeBaseID,
			&document.OriginalFilename,
			&document.ContentType,
			&document.SizeBytes,
			&document.ProcessingStatus,
		); err != nil {
			return nil, fmt.Errorf("scan document: %w", err)
		}
		documents = append(documents, document)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate documents: %w", err)
	}
	return documents, nil
}

func (s *PostgresStore) Create(ctx context.Context, input CreateInput) (Document, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Document{}, fmt.Errorf("begin document transaction: %w", err)
	}
	defer tx.Rollback()
	var document Document
	err = tx.QueryRowContext(ctx, `
		INSERT INTO documents (knowledge_base_id, original_filename, storage_path, content_type, size_bytes)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, knowledge_base_id, original_filename, content_type, size_bytes`,
		input.KnowledgeBaseID, input.OriginalFilename, input.StoragePath, input.ContentType, input.SizeBytes,
	).Scan(&document.ID, &document.KnowledgeBaseID, &document.OriginalFilename, &document.ContentType, &document.SizeBytes)
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23503" {
			return Document{}, ErrKnowledgeBaseNotFound
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

func extensionForContentType(contentType string) (string, bool) {
	switch contentType {
	case "text/markdown":
		return ".md", true
	case "text/plain":
		return ".txt", true
	case "application/pdf":
		return ".pdf", true
	default:
		return "", false
	}
}
