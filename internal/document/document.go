package document

import (
	"bytes"
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
	"sort"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/documentchunk"
	"github.com/bArtyom/n2sql-agent/internal/documentextractor"
	"github.com/bArtyom/n2sql-agent/internal/documentsummary"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
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
	ErrInvalidFolderPath     = errors.New("invalid document folder path")
	ErrInvalidProcessConfig  = errors.New("invalid document process config")
	ErrNoDocumentsSelected   = errors.New("no documents selected")
	ErrFolderMoveConflict    = errors.New("folder destination is inside the source folder")
)

const (
	MaxFolderDepth        = 16
	MaxFolderPathBytes    = 1024
	MaxFolderSegmentBytes = 128
)

type Document struct {
	ID                  int64                          `json:"id"`
	KnowledgeBaseID     int64                          `json:"knowledgeBaseId"`
	OriginalFilename    string                         `json:"originalFilename"`
	FolderPath          string                         `json:"folderPath"`
	ContentType         string                         `json:"contentType"`
	SizeBytes           int64                          `json:"sizeBytes"`
	ContentSHA256       string                         `json:"contentSha256,omitempty"`
	ProcessingStatus    string                         `json:"processingStatus"`
	SummaryStatus       string                         `json:"summaryStatus,omitempty"`
	SummaryIndexStatus  string                         `json:"summaryIndexStatus,omitempty"`
	ChunkingDiagnostics documentchunk.SplitDiagnostics `json:"chunkingDiagnostics"`
	ParserMetadata      map[string]string              `json:"parserMetadata,omitempty"`
}

// Summary is the cached, document-level summary generated on demand.
type Summary = documentsummary.Summary

type UploadInput struct {
	KnowledgeBaseID  int64
	OriginalFilename string
	FolderPath       string
	ContentType      string
	Content          io.Reader
	ProcessConfig    *documentextractor.ProcessConfig
}

type Uploader interface {
	Upload(context.Context, UploadInput) (Document, error)
}

// Deleter removes a document and its derived database records.
type Deleter interface {
	Delete(context.Context, int64, int64) error
}

type Reprocessor interface {
	Reprocess(context.Context, int64, int64, *documentextractor.ProcessConfig) error
}

type BatchReprocessor interface {
	ReprocessMany(context.Context, int64, []int64, *documentextractor.ProcessConfig) (int, error)
}

type CreateInput struct {
	KnowledgeBaseID  int64
	OriginalFilename string
	FolderPath       string
	StoragePath      string
	ContentType      string
	SizeBytes        int64
	ContentSHA256    string
	ProcessConfig    *documentextractor.ProcessConfig
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

// FolderReader supports both exact-folder and recursive-subtree listings.
// A nil folder path is represented by List and means an unfiltered listing;
// an empty string passed to ListInFolder means the knowledge-base root.
type FolderReader interface {
	ListInFolder(context.Context, int64, string, bool) ([]Document, error)
}

type FolderNode struct {
	Path          string        `json:"path"`
	Name          string        `json:"name"`
	DocumentCount int64         `json:"documentCount"`
	TotalCount    int64         `json:"totalCount"`
	Children      []*FolderNode `json:"children,omitempty"`
}

type FolderTree struct {
	RootDocumentCount  int64         `json:"rootDocumentCount"`
	TotalDocumentCount int64         `json:"totalDocumentCount"`
	Folders            []*FolderNode `json:"folders"`
}

type FolderTreeReader interface {
	ListFolderTree(context.Context, int64) (FolderTree, error)
}

type FolderMover interface {
	MoveToFolder(context.Context, int64, []int64, string) (int64, error)
}

type FolderRenamer interface {
	RenameFolder(context.Context, int64, string, string) (int64, error)
}

type FileStore interface {
	Save(context.Context, string, io.Reader) (string, int64, string, error)
	Delete(context.Context, string) error
}

type Asset struct {
	ID               int64
	OriginalFilename string
	ContentType      string
	SizeBytes        int64
	Content          io.ReadSeeker
	Close            func() error
}

type AssetReader interface {
	OpenAsset(context.Context, int64, int64) (Asset, error)
}

type AssetItemReader interface {
	OpenAssetByID(context.Context, int64, int64, int64) (Asset, error)
}

type AssetInfo struct {
	ID               int64  `json:"id"`
	OriginalFilename string `json:"originalFilename"`
	ContentType      string `json:"contentType"`
	SizeBytes        int64  `json:"sizeBytes"`
	Page             int    `json:"page,omitempty"`
	Source           string `json:"source,omitempty"`
	IsOriginal       bool   `json:"isOriginal,omitempty"`
	URL              string `json:"url"`
}

type AssetListReader interface {
	ListAssets(context.Context, int64, int64) ([]AssetInfo, error)
}

type ParseResultStore interface {
	SaveParseResult(context.Context, int64, documentextractor.ParseResult) error
}

// NormalizeFolderPath canonicalizes a client-supplied relative folder path.
// The empty path is the knowledge-base root. Unlike a filesystem path this is
// only metadata: it is never used to open a local file.
func NormalizeFolderPath(raw string) (string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if raw == "" {
		return "", nil
	}
	segments := make([]string, 0, 8)
	for _, part := range strings.Split(raw, "/") {
		part = strings.TrimSpace(part)
		if part == "." || part == ".." || strings.ContainsAny(part, "\x00\r\n") {
			return "", ErrInvalidFolderPath
		}
		part = strings.TrimRight(part, ". ")
		if part == "" {
			continue
		}
		if part == "." || part == ".." {
			return "", ErrInvalidFolderPath
		}
		if len([]byte(part)) > MaxFolderSegmentBytes {
			return "", ErrInvalidFolderPath
		}
		segments = append(segments, part)
		if len(segments) > MaxFolderDepth {
			return "", ErrInvalidFolderPath
		}
	}
	path := strings.Join(segments, "/")
	if len([]byte(path)) > MaxFolderPathBytes {
		return "", ErrInvalidFolderPath
	}
	return path, nil
}

func folderName(path string) string {
	if index := strings.LastIndex(path, "/"); index >= 0 {
		return path[index+1:]
	}
	return path
}

func folderParent(path string) string {
	if index := strings.LastIndex(path, "/"); index >= 0 {
		return path[:index]
	}
	return ""
}

// BuildFolderTree converts the flat SQL aggregation into the tree consumed by
// the API. Intermediate folders are materialized even when they contain only
// descendants, matching the file-manager behavior of WeKnora.
func BuildFolderTree(counts map[string]int64) FolderTree {
	tree := FolderTree{Folders: make([]*FolderNode, 0)}
	nodes := make(map[string]*FolderNode)
	var ensureNode func(string) *FolderNode
	ensureNode = func(path string) *FolderNode {
		if node, ok := nodes[path]; ok {
			return node
		}
		node := &FolderNode{Path: path, Name: folderName(path)}
		nodes[path] = node
		parent := folderParent(path)
		if parent == "" {
			tree.Folders = append(tree.Folders, node)
		} else {
			parentNode := ensureNode(parent)
			parentNode.Children = append(parentNode.Children, node)
		}
		return node
	}
	for path, count := range counts {
		if path == "" {
			tree.RootDocumentCount += count
			tree.TotalDocumentCount += count
			continue
		}
		node := ensureNode(path)
		node.DocumentCount += count
		tree.TotalDocumentCount += count
	}
	paths := make([]string, 0, len(nodes))
	for path := range nodes {
		paths = append(paths, path)
	}
	sort.Slice(paths, func(i, j int) bool {
		depthI := strings.Count(paths[i], "/")
		depthJ := strings.Count(paths[j], "/")
		if depthI != depthJ {
			return depthI > depthJ
		}
		return paths[i] < paths[j]
	})
	for _, path := range paths {
		node := nodes[path]
		node.TotalCount += node.DocumentCount
		if parent := folderParent(path); parent != "" {
			nodes[parent].TotalCount += node.TotalCount
		}
	}
	var sortNodes func([]*FolderNode)
	sortNodes = func(list []*FolderNode) {
		sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
		for _, node := range list {
			sortNodes(node.Children)
		}
	}
	sortNodes(tree.Folders)
	return tree
}

// FolderPathInScope reports whether a document path belongs to a selected
// folder. The root folder is represented by an empty path and only contains
// root-level documents; recursive selection includes descendants.
func FolderPathInScope(path, scope string, recursive bool) bool {
	if path == scope {
		return true
	}
	return recursive && scope != "" && strings.HasPrefix(path, scope+"/")
}

type assetMetadataStore interface {
	AssetMetadata(context.Context, int64, int64) (string, string, string, int64, error)
}

type assetMetadataByIDStore interface {
	AssetMetadataByID(context.Context, int64, int64, int64) (string, string, string, int64, error)
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
	folderPath, err := NormalizeFolderPath(input.FolderPath)
	if err != nil {
		return Document{}, err
	}
	if err := s.store.EnsureKnowledgeBase(ctx, input.KnowledgeBaseID); err != nil {
		return Document{}, err
	}
	if err := documentextractor.ValidateProcessConfig(input.ProcessConfig); err != nil {
		return Document{}, fmt.Errorf("%w: %v", ErrInvalidProcessConfig, err)
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
		FolderPath:       folderPath,
		StoragePath:      storagePath,
		ContentType:      input.ContentType,
		SizeBytes:        sizeBytes,
		ContentSHA256:    contentSHA256,
		ProcessConfig:    input.ProcessConfig,
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

func (s *Service) OpenAssetByID(ctx context.Context, knowledgeBaseID, documentID, assetID int64) (Asset, error) {
	metadataStore, ok := s.store.(assetMetadataByIDStore)
	if !ok {
		return Asset{}, ErrUnsupportedFile
	}
	opener, ok := s.files.(assetFileOpener)
	if !ok {
		return Asset{}, ErrUnsupportedFile
	}
	storagePath, filename, contentType, sizeBytes, err := metadataStore.AssetMetadataByID(ctx, knowledgeBaseID, documentID, assetID)
	if err != nil {
		return Asset{}, err
	}
	content, closeFile, err := opener.Open(ctx, storagePath)
	if err != nil {
		return Asset{}, err
	}
	return Asset{ID: assetID, OriginalFilename: filename, ContentType: contentType, SizeBytes: sizeBytes, Content: content, Close: closeFile}, nil
}

func (s *Service) ListAssets(ctx context.Context, knowledgeBaseID, documentID int64) ([]AssetInfo, error) {
	reader, ok := s.store.(AssetListReader)
	if !ok {
		return nil, ErrUnsupportedFile
	}
	return reader.ListAssets(ctx, knowledgeBaseID, documentID)
}

func (s *Service) SaveParseResult(ctx context.Context, documentID int64, result documentextractor.ParseResult) error {
	store, ok := s.store.(ParseResultStore)
	if !ok {
		return ErrUnsupportedFile
	}
	return store.SaveParseResult(ctx, documentID, result)
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

func (s *Service) ListInFolder(ctx context.Context, knowledgeBaseID int64, folderPath string, recursive bool) ([]Document, error) {
	if knowledgeBaseID <= 0 {
		return nil, ErrKnowledgeBaseNotFound
	}
	normalized, err := NormalizeFolderPath(folderPath)
	if err != nil {
		return nil, err
	}
	if err := s.store.EnsureKnowledgeBase(ctx, knowledgeBaseID); err != nil {
		return nil, err
	}
	reader, ok := s.store.(FolderReader)
	if !ok {
		return nil, ErrUnsupportedFile
	}
	return reader.ListInFolder(ctx, knowledgeBaseID, normalized, recursive)
}

func (s *Service) ListFolderTree(ctx context.Context, knowledgeBaseID int64) (FolderTree, error) {
	if knowledgeBaseID <= 0 {
		return FolderTree{}, ErrKnowledgeBaseNotFound
	}
	if err := s.store.EnsureKnowledgeBase(ctx, knowledgeBaseID); err != nil {
		return FolderTree{}, err
	}
	reader, ok := s.store.(FolderTreeReader)
	if !ok {
		return FolderTree{}, ErrUnsupportedFile
	}
	return reader.ListFolderTree(ctx, knowledgeBaseID)
}

func (s *Service) MoveToFolder(ctx context.Context, knowledgeBaseID int64, documentIDs []int64, folderPath string) (int64, error) {
	if knowledgeBaseID <= 0 {
		return 0, ErrKnowledgeBaseNotFound
	}
	if len(documentIDs) == 0 {
		return 0, ErrNoDocumentsSelected
	}
	for _, documentID := range documentIDs {
		if documentID <= 0 {
			return 0, ErrDocumentNotFound
		}
	}
	normalized, err := NormalizeFolderPath(folderPath)
	if err != nil {
		return 0, err
	}
	if err := s.store.EnsureKnowledgeBase(ctx, knowledgeBaseID); err != nil {
		return 0, err
	}
	mover, ok := s.store.(FolderMover)
	if !ok {
		return 0, ErrUnsupportedFile
	}
	moved, err := mover.MoveToFolder(ctx, knowledgeBaseID, documentIDs, normalized)
	if err == nil && moved > 0 && s.invalidator != nil {
		s.invalidator.ClearCache(knowledgeBaseID)
	}
	return moved, err
}

func (s *Service) RenameFolder(ctx context.Context, knowledgeBaseID int64, from, to string) (int64, error) {
	if knowledgeBaseID <= 0 {
		return 0, ErrKnowledgeBaseNotFound
	}
	from, err := NormalizeFolderPath(from)
	if err != nil || from == "" {
		return 0, ErrInvalidFolderPath
	}
	to, err = NormalizeFolderPath(to)
	if err != nil || to == "" {
		return 0, ErrInvalidFolderPath
	}
	if from == to || strings.HasPrefix(to, from+"/") {
		return 0, ErrFolderMoveConflict
	}
	if err := s.store.EnsureKnowledgeBase(ctx, knowledgeBaseID); err != nil {
		return 0, err
	}
	renamer, ok := s.store.(FolderRenamer)
	if !ok {
		return 0, ErrUnsupportedFile
	}
	moved, err := renamer.RenameFolder(ctx, knowledgeBaseID, from, to)
	if err == nil && moved > 0 && s.invalidator != nil {
		s.invalidator.ClearCache(knowledgeBaseID)
	}
	return moved, err
}

func (s *Service) Reprocess(ctx context.Context, knowledgeBaseID, documentID int64, config *documentextractor.ProcessConfig) error {
	if knowledgeBaseID <= 0 || documentID <= 0 {
		return ErrDocumentNotFound
	}
	if err := documentextractor.ValidateProcessConfig(config); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidProcessConfig, err)
	}
	reprocessor, ok := s.store.(Reprocessor)
	if !ok {
		return ErrDeleteUnavailable
	}
	return reprocessor.Reprocess(ctx, knowledgeBaseID, documentID, config)
}

func (s *Service) ReprocessMany(ctx context.Context, knowledgeBaseID int64, documentIDs []int64, config *documentextractor.ProcessConfig) (int, error) {
	if knowledgeBaseID <= 0 {
		return 0, ErrKnowledgeBaseNotFound
	}
	normalized, err := normalizeDocumentIDs(documentIDs)
	if err != nil {
		return 0, err
	}
	if err := documentextractor.ValidateProcessConfig(config); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrInvalidProcessConfig, err)
	}
	if err := s.store.EnsureKnowledgeBase(ctx, knowledgeBaseID); err != nil {
		return 0, err
	}
	reprocessor, ok := s.store.(BatchReprocessor)
	if !ok {
		return 0, ErrDeleteUnavailable
	}
	return reprocessor.ReprocessMany(ctx, knowledgeBaseID, normalized, config)
}

func normalizeDocumentIDs(documentIDs []int64) ([]int64, error) {
	if len(documentIDs) == 0 {
		return nil, ErrNoDocumentsSelected
	}
	ids := append([]int64(nil), documentIDs...)
	for _, documentID := range ids {
		if documentID <= 0 {
			return nil, ErrDocumentNotFound
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	unique := ids[:0]
	for _, documentID := range ids {
		if len(unique) == 0 || unique[len(unique)-1] != documentID {
			unique = append(unique, documentID)
		}
	}
	return unique, nil
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

type PostgresStore struct {
	db    *sql.DB
	files FileStore
}

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func NewPostgresStoreWithFileStore(db *sql.DB, files FileStore) *PostgresStore {
	return &PostgresStore{db: db, files: files}
}

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

func (s *PostgresStore) AssetMetadataByID(ctx context.Context, knowledgeBaseID, documentID, assetID int64) (string, string, string, int64, error) {
	var storagePath, filename, contentType string
	var sizeBytes int64
	err := s.db.QueryRowContext(ctx, `
		SELECT a.storage_path, a.original_filename, a.content_type, a.size_bytes
		FROM document_assets AS a
		JOIN documents AS d ON d.id = a.document_id
		JOIN knowledge_bases AS kb ON kb.id = d.knowledge_base_id
		WHERE a.id = $1 AND a.document_id = $2 AND d.knowledge_base_id = $3
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, assetID, documentID, knowledgeBaseID).
		Scan(&storagePath, &filename, &contentType, &sizeBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", 0, ErrDocumentNotFound
	}
	if err != nil {
		return "", "", "", 0, fmt.Errorf("read document asset metadata: %w", err)
	}
	return storagePath, filename, contentType, sizeBytes, nil
}

func (s *PostgresStore) ListAssets(ctx context.Context, knowledgeBaseID, documentID int64) ([]AssetInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id, a.original_filename, a.content_type, a.size_bytes, a.page, a.source, a.is_original
		FROM document_assets AS a
		JOIN documents AS d ON d.id = a.document_id
		JOIN knowledge_bases AS kb ON kb.id = d.knowledge_base_id
		WHERE a.document_id = $1 AND d.knowledge_base_id = $2
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		ORDER BY a.asset_index, a.id`, documentID, knowledgeBaseID)
	if err != nil {
		return nil, fmt.Errorf("list document assets: %w", err)
	}
	defer rows.Close()
	assets := make([]AssetInfo, 0)
	for rows.Next() {
		var asset AssetInfo
		if err := rows.Scan(&asset.ID, &asset.OriginalFilename, &asset.ContentType, &asset.SizeBytes, &asset.Page, &asset.Source, &asset.IsOriginal); err != nil {
			return nil, fmt.Errorf("scan document asset: %w", err)
		}
		asset.URL = fmt.Sprintf("/api/knowledge-bases/%d/documents/%d/assets/%d", knowledgeBaseID, documentID, asset.ID)
		assets = append(assets, asset)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate document assets: %w", err)
	}
	return assets, nil
}

func (s *PostgresStore) AssetURLs(ctx context.Context, knowledgeBaseID, documentID int64) ([]string, error) {
	assets, err := s.ListAssets(ctx, knowledgeBaseID, documentID)
	if err != nil {
		return nil, err
	}
	urls := make([]string, 0, len(assets))
	for _, asset := range assets {
		urls = append(urls, asset.URL)
	}
	return urls, nil
}

func (s *PostgresStore) SaveParseResult(ctx context.Context, documentID int64, result documentextractor.ParseResult) error {
	if s.files == nil {
		return ErrUnsupportedFile
	}
	metadata, err := json.Marshal(result.Metadata)
	if err != nil {
		return fmt.Errorf("encode parser metadata: %w", err)
	}
	var originalPath, originalName, originalType string
	var originalSize int64
	if err := s.db.QueryRowContext(ctx, `SELECT storage_path, original_filename, content_type, size_bytes FROM documents WHERE id = $1`, documentID).Scan(&originalPath, &originalName, &originalType, &originalSize); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDocumentNotFound
		}
		return fmt.Errorf("read document for parser result: %w", err)
	}

	type storedAsset struct {
		index                    int
		name, path, mime, source string
		page                     int
		size                     int64
		original                 bool
	}
	stored := make([]storedAsset, 0, len(result.Images))
	newPaths := make([]string, 0)
	cleanupNew := func() {
		for _, path := range newPaths {
			_ = s.files.Delete(context.WithoutCancel(ctx), path)
		}
	}
	for index, image := range result.Images {
		if !strings.HasPrefix(image.MIMEType, "image/") || len(image.Data) == 0 || int64(len(image.Data)) > MaxFileBytes {
			cleanupNew()
			return fmt.Errorf("invalid parser image asset at index %d", index)
		}
		asset := storedAsset{index: index, name: image.Filename, mime: image.MIMEType, source: image.Source, page: image.Page, size: int64(len(image.Data)), original: image.Original}
		if asset.name == "" {
			asset.name = fmt.Sprintf("image-%d", index+1)
		}
		if image.Original {
			asset.path, asset.name, asset.mime, asset.size = originalPath, originalName, originalType, originalSize
		} else {
			extension, ok := extensionForContentType(image.MIMEType)
			if !ok {
				cleanupNew()
				return fmt.Errorf("unsupported parser image MIME type %q", image.MIMEType)
			}
			path, size, _, saveErr := s.files.Save(ctx, extension, bytes.NewReader(image.Data))
			if saveErr != nil {
				cleanupNew()
				return fmt.Errorf("save parser image asset: %w", saveErr)
			}
			asset.path, asset.size = path, size
			newPaths = append(newPaths, path)
		}
		stored = append(stored, asset)
	}

	rows, err := s.db.QueryContext(ctx, `SELECT storage_path, is_original FROM document_assets WHERE document_id = $1`, documentID)
	if err != nil {
		cleanupNew()
		return fmt.Errorf("read old document assets: %w", err)
	}
	var oldPaths []string
	for rows.Next() {
		var path string
		var isOriginal bool
		if scanErr := rows.Scan(&path, &isOriginal); scanErr != nil {
			rows.Close()
			cleanupNew()
			return fmt.Errorf("scan old document asset: %w", scanErr)
		}
		if !isOriginal {
			oldPaths = append(oldPaths, path)
		}
	}
	if err := rows.Close(); err != nil {
		cleanupNew()
		return fmt.Errorf("close old document assets: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		cleanupNew()
		return fmt.Errorf("begin parser result transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE documents SET parser_metadata = $2::jsonb WHERE id = $1`, documentID, metadata); err != nil {
		cleanupNew()
		return fmt.Errorf("save parser metadata: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM document_assets WHERE document_id = $1`, documentID); err != nil {
		cleanupNew()
		return fmt.Errorf("replace document assets: %w", err)
	}
	for _, asset := range stored {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO document_assets (document_id, asset_index, original_filename, storage_path, content_type, size_bytes, page, source, is_original)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, documentID, asset.index, asset.name, asset.path, asset.mime, asset.size, asset.page, asset.source, asset.original); err != nil {
			cleanupNew()
			return fmt.Errorf("insert document asset: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		cleanupNew()
		return fmt.Errorf("commit parser result: %w", err)
	}
	for _, path := range oldPaths {
		_ = s.files.Delete(context.WithoutCancel(ctx), path)
	}
	return nil
}

func (s *PostgresStore) List(ctx context.Context, knowledgeBaseID int64) ([]Document, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.id, d.knowledge_base_id, d.original_filename, d.folder_path, d.content_type, d.size_bytes, d.content_sha256,
		       d.chunking_diagnostics, d.parser_metadata, d.summary_status, d.summary_index_status,
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
		var diagnostics, parserMetadata []byte
		if err := rows.Scan(
			&document.ID,
			&document.KnowledgeBaseID,
			&document.OriginalFilename,
			&document.FolderPath,
			&document.ContentType,
			&document.SizeBytes,
			&document.ContentSHA256,
			&diagnostics,
			&parserMetadata,
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
		if len(parserMetadata) > 0 {
			if err := json.Unmarshal(parserMetadata, &document.ParserMetadata); err != nil {
				return nil, fmt.Errorf("decode parser metadata: %w", err)
			}
		}
		documents = append(documents, document)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate documents: %w", err)
	}
	return documents, nil
}

func (s *PostgresStore) ListInFolder(ctx context.Context, knowledgeBaseID int64, folderPath string, recursive bool) ([]Document, error) {
	condition := "d.folder_path = $2"
	if recursive && folderPath != "" {
		condition = "(d.folder_path = $2 OR LEFT(d.folder_path, LENGTH($2) + 1) = $2 || '/')"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.id, d.knowledge_base_id, d.original_filename, d.folder_path, d.content_type, d.size_bytes, d.content_sha256,
		       d.chunking_diagnostics, d.parser_metadata, d.summary_status, d.summary_index_status,
		       COALESCE(task.status, 'pending') AS processing_status
		FROM documents AS d
		LEFT JOIN LATERAL (
			SELECT status FROM document_processing_tasks
			WHERE document_id = d.id
			ORDER BY created_at DESC, id DESC LIMIT 1
		) AS task ON TRUE
		WHERE d.knowledge_base_id = $1
		  AND `+condition+`
		  AND d.knowledge_base_id IN (
			SELECT id FROM knowledge_bases
			WHERE administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		  )
		ORDER BY d.created_at DESC, d.id DESC`, knowledgeBaseID, folderPath)
	if err != nil {
		return nil, fmt.Errorf("list documents in folder: %w", err)
	}
	defer rows.Close()
	return scanDocuments(rows)
}

func scanDocuments(rows *sql.Rows) ([]Document, error) {
	documents := make([]Document, 0)
	for rows.Next() {
		var document Document
		var diagnostics, parserMetadata []byte
		if err := rows.Scan(
			&document.ID, &document.KnowledgeBaseID, &document.OriginalFilename,
			&document.FolderPath, &document.ContentType, &document.SizeBytes,
			&document.ContentSHA256, &diagnostics, &parserMetadata,
			&document.SummaryStatus, &document.SummaryIndexStatus, &document.ProcessingStatus,
		); err != nil {
			return nil, fmt.Errorf("scan document: %w", err)
		}
		if len(diagnostics) > 0 {
			if err := json.Unmarshal(diagnostics, &document.ChunkingDiagnostics); err != nil {
				return nil, fmt.Errorf("decode chunking diagnostics: %w", err)
			}
		}
		if len(parserMetadata) > 0 {
			if err := json.Unmarshal(parserMetadata, &document.ParserMetadata); err != nil {
				return nil, fmt.Errorf("decode parser metadata: %w", err)
			}
		}
		documents = append(documents, document)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate documents: %w", err)
	}
	return documents, nil
}

func (s *PostgresStore) ListFolderTree(ctx context.Context, knowledgeBaseID int64) (FolderTree, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.folder_path, COUNT(*)
		FROM documents AS d
		JOIN knowledge_bases AS kb ON kb.id = d.knowledge_base_id
		WHERE d.knowledge_base_id = $1
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		GROUP BY d.folder_path`, knowledgeBaseID)
	if err != nil {
		return FolderTree{}, fmt.Errorf("list document folders: %w", err)
	}
	defer rows.Close()
	counts := make(map[string]int64)
	for rows.Next() {
		var path string
		var count int64
		if err := rows.Scan(&path, &count); err != nil {
			return FolderTree{}, fmt.Errorf("scan document folder: %w", err)
		}
		counts[path] = count
	}
	if err := rows.Err(); err != nil {
		return FolderTree{}, fmt.Errorf("iterate document folders: %w", err)
	}
	return BuildFolderTree(counts), nil
}

func (s *PostgresStore) MoveToFolder(ctx context.Context, knowledgeBaseID int64, documentIDs []int64, folderPath string) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE documents AS d SET folder_path = $3
		FROM knowledge_bases AS kb
		WHERE d.knowledge_base_id = kb.id
		  AND d.knowledge_base_id = $1
		  AND d.id = ANY($2::bigint[])
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, knowledgeBaseID, documentIDs, folderPath)
	if err != nil {
		return 0, fmt.Errorf("move documents to folder: %w", err)
	}
	count, err := result.RowsAffected()
	return count, err
}

func (s *PostgresStore) RenameFolder(ctx context.Context, knowledgeBaseID int64, from, to string) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE documents AS d SET folder_path = $3 || CASE
			WHEN d.folder_path = $2 THEN ''
			ELSE SUBSTRING(d.folder_path FROM LENGTH($2) + 1)
		END
		FROM knowledge_bases AS kb
		WHERE d.knowledge_base_id = kb.id
		  AND d.knowledge_base_id = $1
		  AND (d.folder_path = $2 OR LEFT(d.folder_path, LENGTH($2) + 1) = $2 || '/')
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, knowledgeBaseID, from, to)
	if err != nil {
		return 0, fmt.Errorf("rename document folder: %w", err)
	}
	count, err := result.RowsAffected()
	return count, err
}

func (s *PostgresStore) Reprocess(ctx context.Context, knowledgeBaseID, documentID int64, config *documentextractor.ProcessConfig) error {
	if err := documentextractor.ValidateProcessConfig(config); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidProcessConfig, err)
	}
	processConfig := []byte("{}")
	if config != nil {
		var marshalErr error
		processConfig, marshalErr = json.Marshal(config)
		if marshalErr != nil {
			return fmt.Errorf("encode reprocess config: %w", marshalErr)
		}
	}
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
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO document_processing_tasks (document_id, process_config)
		SELECT $1, CASE WHEN $2::boolean THEN $3::jsonb ELSE COALESCE(
			(SELECT process_config FROM document_processing_tasks
			 WHERE document_id = $1 ORDER BY created_at DESC, id DESC LIMIT 1),
			'{}'::jsonb
		) END`, documentID, config != nil, processConfig); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.ConstraintName == "document_processing_tasks_active_document_idx" {
			return ErrDocumentProcessing
		}
		return fmt.Errorf("create document reprocess task: %w", err)
	}
	return nil
}

func (s *PostgresStore) ReprocessMany(ctx context.Context, knowledgeBaseID int64, documentIDs []int64, config *documentextractor.ProcessConfig) (int, error) {
	if err := documentextractor.ValidateProcessConfig(config); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrInvalidProcessConfig, err)
	}
	normalized, err := normalizeDocumentIDs(documentIDs)
	if err != nil {
		return 0, err
	}
	processConfig := []byte("{}")
	if config != nil {
		processConfig, err = json.Marshal(config)
		if err != nil {
			return 0, fmt.Errorf("encode batch reprocess config: %w", err)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin batch reprocess: %w", err)
	}
	defer tx.Rollback()
	var selected int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM documents AS d
		JOIN knowledge_bases AS kb ON kb.id = d.knowledge_base_id
		WHERE d.knowledge_base_id = $1 AND d.id = ANY($2::bigint[])
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, knowledgeBaseID, pq.Array(normalized)).Scan(&selected); err != nil {
		return 0, fmt.Errorf("check batch reprocess documents: %w", err)
	}
	if selected != len(normalized) {
		return 0, ErrDocumentNotFound
	}
	var active int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT task.document_id)
		FROM document_processing_tasks AS task
		JOIN documents AS d ON d.id = task.document_id
		JOIN knowledge_bases AS kb ON kb.id = d.knowledge_base_id
		WHERE d.knowledge_base_id = $1 AND task.document_id = ANY($2::bigint[])
		  AND task.status IN ('pending', 'processing')
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, knowledgeBaseID, pq.Array(normalized)).Scan(&active); err != nil {
		return 0, fmt.Errorf("check active batch reprocess tasks: %w", err)
	}
	if active > 0 {
		return 0, ErrDocumentProcessing
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO document_processing_tasks (document_id, process_config)
		SELECT d.id, CASE WHEN $3::boolean THEN $4::jsonb ELSE COALESCE(
			(SELECT previous.process_config FROM document_processing_tasks AS previous
			 WHERE previous.document_id = d.id ORDER BY previous.created_at DESC, previous.id DESC LIMIT 1),
			'{}'::jsonb
		) END
		FROM documents AS d
		JOIN knowledge_bases AS kb ON kb.id = d.knowledge_base_id
		WHERE d.knowledge_base_id = $1 AND d.id = ANY($2::bigint[])
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)`, knowledgeBaseID, pq.Array(normalized), config != nil, processConfig)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.ConstraintName == "document_processing_tasks_active_document_idx" {
			return 0, ErrDocumentProcessing
		}
		return 0, fmt.Errorf("create batch document processing tasks: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count batch reprocess tasks: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit batch reprocess: %w", err)
	}
	return int(count), nil
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
	if err := documentextractor.ValidateProcessConfig(input.ProcessConfig); err != nil {
		return Document{}, fmt.Errorf("%w: %v", ErrInvalidProcessConfig, err)
	}
	processConfig := []byte("{}")
	if input.ProcessConfig != nil {
		var marshalErr error
		processConfig, marshalErr = json.Marshal(input.ProcessConfig)
		if marshalErr != nil {
			return Document{}, fmt.Errorf("encode document process config: %w", marshalErr)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Document{}, fmt.Errorf("begin document transaction: %w", err)
	}
	defer tx.Rollback()
	var document Document
	err = tx.QueryRowContext(ctx, `
		INSERT INTO documents (knowledge_base_id, original_filename, folder_path, storage_path, content_type, size_bytes, content_sha256)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, knowledge_base_id, original_filename, folder_path, content_type, size_bytes, content_sha256`,
		input.KnowledgeBaseID, input.OriginalFilename, input.FolderPath, input.StoragePath, input.ContentType, input.SizeBytes, input.ContentSHA256,
	).Scan(&document.ID, &document.KnowledgeBaseID, &document.OriginalFilename, &document.FolderPath, &document.ContentType, &document.SizeBytes, &document.ContentSHA256)
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
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO document_processing_tasks (document_id, process_config)
		SELECT $1,
		       CASE WHEN $2::boolean THEN $3::jsonb
		            ELSE jsonb_build_object('parser_engine_rules', knowledge_base.parser_engine_rules)
		       END
		FROM knowledge_bases AS knowledge_base
		WHERE knowledge_base.id = $4`, document.ID, input.ProcessConfig != nil, processConfig, input.KnowledgeBaseID); err != nil {
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
