package postprocess

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	defaultLeaseDuration = 5 * time.Minute
	maxResultBytes       = 128 << 10
)

type PostgresStore struct {
	db            *sql.DB
	leaseDuration time.Duration
}

func (s *PostgresStore) PendingCount(ctx context.Context) (int64, error) {
	var count int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM document_postprocess_tasks WHERE status = 'pending' AND next_attempt_at <= CURRENT_TIMESTAMP`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count document postprocess tasks: %w", err)
	}
	return count, nil
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return NewPostgresStoreWithLease(db, defaultLeaseDuration)
}

func NewPostgresStoreWithLease(db *sql.DB, leaseDuration time.Duration) *PostgresStore {
	if leaseDuration <= 0 {
		leaseDuration = defaultLeaseDuration
	}
	return &PostgresStore{db: db, leaseDuration: leaseDuration}
}

func (s *PostgresStore) Enqueue(ctx context.Context, requests ...EnqueueRequest) error {
	if s == nil || s.db == nil || len(requests) == 0 {
		return errors.New("invalid postprocess enqueue request")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin postprocess enqueue: %w", err)
	}
	defer tx.Rollback()
	for _, request := range requests {
		if strings.TrimSpace(request.TaskKey) == "" || request.Kind == "" || request.KnowledgeBaseID <= 0 {
			return errors.New("invalid postprocess task")
		}
		payload := request.Payload
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		chunkPosition := request.ChunkPosition
		if request.Kind != KindGraphExtract {
			chunkPosition = -1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO document_postprocess_tasks
				(task_key, knowledge_base_id, document_id, asset_id, asset_index, chunk_position, task_kind, payload)
			VALUES ($1, $2, NULLIF($3, 0), NULLIF($4, 0), $5, NULLIF($6, -1), $7, $8::jsonb)
			ON CONFLICT (task_key) DO NOTHING`, request.TaskKey, request.KnowledgeBaseID, request.DocumentID, request.AssetID, request.AssetIndex, chunkPosition, request.Kind, string(payload)); err != nil {
			return fmt.Errorf("enqueue postprocess task %s: %w", request.TaskKey, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit postprocess enqueue: %w", err)
	}
	return nil
}

func (s *PostgresStore) EnqueueDocument(ctx context.Context, documentID int64, options DocumentOptions) error {
	if s == nil || s.db == nil || documentID <= 0 {
		return errors.New("invalid document postprocess enqueue request")
	}
	if !options.EnableSummary && !options.EnableImageOCR && !options.EnableImageCaption && !options.EnableRecommendations && !options.EnableGraph {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.knowledge_base_id, a.id, a.asset_index, a.ocr_text, a.caption,
			d.original_filename, a.original_filename, a.content_type, a.page, a.source,
			a.bounds, a.block_order, a.span_id
		FROM documents AS d
		LEFT JOIN document_assets AS a ON a.document_id = d.id
		WHERE d.id = $1
		ORDER BY a.asset_index, a.id`, documentID)
	if err != nil {
		return fmt.Errorf("read document assets for postprocess: %w", err)
	}
	defer rows.Close()
	var knowledgeBaseID int64
	var requests []EnqueueRequest
	if options.EnableSummary || options.EnableRecommendations {
		// The document row is returned even when there are no assets.
		requests = make([]EnqueueRequest, 0, 4)
	}
	if options.EnableSummary {
		payload, _ := json.Marshal(map[string]bool{"enable_recommendations": options.EnableRecommendations})
		requests = append(requests, EnqueueRequest{TaskKey: fmt.Sprintf("document-summary:%d", documentID), KnowledgeBaseID: 0, DocumentID: documentID, Kind: KindDocumentSummary, Payload: payload})
	}
	for rows.Next() {
		var assetID, assetIndex sql.NullInt64
		var ocrText, caption sql.NullString
		var originalFilename, assetFilename, contentType, source, spanID sql.NullString
		var page, blockOrder sql.NullInt64
		var bounds []byte
		if err := rows.Scan(&knowledgeBaseID, &assetID, &assetIndex, &ocrText, &caption, &originalFilename, &assetFilename, &contentType, &page, &source, &bounds, &blockOrder, &spanID); err != nil {
			return fmt.Errorf("scan document asset for postprocess: %w", err)
		}
		if !assetID.Valid {
			continue
		}
		payload := map[string]any{"original_filename": originalFilename.String, "filename": assetFilename.String, "content_type": contentType.String, "page": page.Int64, "source": source.String, "block_order": blockOrder.Int64, "span_id": spanID.String}
		if len(bounds) > 0 {
			var decodedBounds [4]int
			if json.Unmarshal(bounds, &decodedBounds) == nil {
				payload["bounds"] = decodedBounds
			}
		}
		if options.EnableImageOCR || ocrText.Valid && strings.TrimSpace(ocrText.String) != "" {
			payload["seed_result"] = strings.TrimSpace(ocrText.String)
			encodedPayload, _ := json.Marshal(payload)
			request := EnqueueRequest{TaskKey: fmt.Sprintf("image-ocr:%d", assetID.Int64), KnowledgeBaseID: knowledgeBaseID, DocumentID: documentID, AssetID: assetID.Int64, AssetIndex: int(assetIndex.Int64), Kind: KindImageOCR, Payload: encodedPayload}
			requests = append(requests, request)
		}
		if options.EnableImageCaption || caption.Valid && strings.TrimSpace(caption.String) != "" {
			payload["seed_result"] = strings.TrimSpace(caption.String)
			encodedPayload, _ := json.Marshal(payload)
			request := EnqueueRequest{TaskKey: fmt.Sprintf("image-caption:%d", assetID.Int64), KnowledgeBaseID: knowledgeBaseID, DocumentID: documentID, AssetID: assetID.Int64, AssetIndex: int(assetIndex.Int64), Kind: KindImageCaption, Payload: encodedPayload}
			requests = append(requests, request)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate document assets for postprocess: %w", err)
	}
	if options.EnableGraph {
		chunkRows, chunkErr := s.db.QueryContext(ctx, `
			SELECT d.knowledge_base_id, COALESCE(d.current_version_id, 0), chunks.position
			FROM documents AS d
			JOIN document_chunks AS chunks ON chunks.document_id = d.id
			WHERE d.id = $1 AND chunks.chunk_kind = 'text'
			ORDER BY chunks.position`, documentID)
		if chunkErr != nil {
			return fmt.Errorf("read document chunks for graph extraction: %w", chunkErr)
		}
		for chunkRows.Next() {
			var chunkKnowledgeBaseID, versionID, position int64
			if err := chunkRows.Scan(&chunkKnowledgeBaseID, &versionID, &position); err != nil {
				chunkRows.Close()
				return fmt.Errorf("scan document chunk for graph extraction: %w", err)
			}
			requests = append(requests, EnqueueRequest{
				TaskKey:         fmt.Sprintf("graph-extract:%d:%d:%d", documentID, versionID, position),
				KnowledgeBaseID: chunkKnowledgeBaseID,
				DocumentID:      documentID,
				ChunkPosition:   int(position),
				Kind:            KindGraphExtract,
			})
		}
		if err := chunkRows.Err(); err != nil {
			chunkRows.Close()
			return fmt.Errorf("iterate document chunks for graph extraction: %w", err)
		}
		chunkRows.Close()
	}
	if knowledgeBaseID == 0 {
		rowErr := s.db.QueryRowContext(ctx, `SELECT knowledge_base_id FROM documents WHERE id = $1`, documentID).Scan(&knowledgeBaseID)
		if rowErr != nil {
			return fmt.Errorf("read document knowledge base for postprocess: %w", rowErr)
		}
	}
	for index := range requests {
		if requests[index].KnowledgeBaseID == 0 {
			requests[index].KnowledgeBaseID = knowledgeBaseID
		}
	}
	if len(requests) == 0 {
		return nil
	}
	return s.Enqueue(ctx, requests...)
}

func (s *PostgresStore) ClaimNext(ctx context.Context) (Task, error) {
	if s == nil || s.db == nil {
		return Task{}, errors.New("postprocess store is unavailable")
	}
	token, err := newLeaseToken()
	if err != nil {
		return Task{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Task{}, fmt.Errorf("begin postprocess claim: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE document_postprocess_tasks
		SET status = 'pending', lease_token = NULL, lease_until = NULL,
			next_attempt_at = CURRENT_TIMESTAMP,
			error_message = COALESCE(error_message, 'postprocess task lease expired')
		WHERE status = 'processing' AND lease_until IS NOT NULL AND lease_until < CURRENT_TIMESTAMP`); err != nil {
		return Task{}, fmt.Errorf("requeue expired postprocess tasks: %w", err)
	}
	leaseSeconds := int64(s.leaseDuration / time.Second)
	if leaseSeconds <= 0 {
		leaseSeconds = 1
	}
	var task Task
	var documentID, assetID sql.NullInt64
	var payload []byte
	err = tx.QueryRowContext(ctx, `
		WITH next_task AS (
			SELECT id
			FROM document_postprocess_tasks
			WHERE status = 'pending' AND next_attempt_at <= CURRENT_TIMESTAMP
			ORDER BY created_at, id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE document_postprocess_tasks AS task
		SET status = 'processing', attempt_count = attempt_count + 1,
			started_at = CURRENT_TIMESTAMP, completed_at = NULL,
			error_message = NULL, lease_token = $1,
			lease_until = CURRENT_TIMESTAMP + ($2 * INTERVAL '1 second')
		FROM next_task
		WHERE task.id = next_task.id
		RETURNING task.id, task.task_key, task.knowledge_base_id, task.document_id,
			task.asset_id, task.asset_index, COALESCE(task.chunk_position, -1), task.task_kind, task.status,
			task.attempt_count, task.payload, task.result_text, task.started_at,
			task.updated_at, task.lease_until, task.lease_token`, token, leaseSeconds).
		Scan(&task.ID, &task.TaskKey, &task.KnowledgeBaseID, &documentID, &assetID, &task.AssetIndex, &task.ChunkPosition, &task.Kind, &task.Status, &task.AttemptCount, &payload, &task.ResultText, &task.StartedAt, &task.UpdatedAt, &task.LeaseUntil, &task.LeaseToken)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, ErrNoTask
	}
	if err != nil {
		return Task{}, fmt.Errorf("claim next postprocess task: %w", err)
	}
	if documentID.Valid {
		task.DocumentID = documentID.Int64
	}
	if assetID.Valid {
		task.AssetID = assetID.Int64
	}
	if task.ChunkPosition < 0 {
		task.ChunkPosition = 0
	}
	task.Payload = json.RawMessage(payload)
	if err := tx.Commit(); err != nil {
		return Task{}, fmt.Errorf("commit postprocess claim: %w", err)
	}
	return task, nil
}

func (s *PostgresStore) SaveResult(ctx context.Context, task Task, result Result) error {
	result.Text = strings.TrimSpace(result.Text)
	if task.ID <= 0 || task.LeaseToken == "" {
		return errors.New("invalid postprocess task lease")
	}
	if len([]byte(result.Text)) > maxResultBytes {
		return fmt.Errorf("postprocess result exceeds %d bytes", maxResultBytes)
	}
	updated, err := s.db.ExecContext(ctx, `
		UPDATE document_postprocess_tasks
		SET result_text = $4, model_provider = NULLIF($5, ''), model_name = NULLIF($6, ''),
			input_tokens = $7, output_tokens = $8, cost_micros = $9, duration_ms = $10
		WHERE id = $1 AND status = 'processing' AND lease_token = $2 AND knowledge_base_id = $3`, task.ID, task.LeaseToken, task.KnowledgeBaseID, result.Text, result.ModelProvider, result.ModelName, result.InputTokens, result.OutputTokens, result.CostMicros, result.DurationMS)
	if err != nil {
		return fmt.Errorf("save postprocess result: %w", err)
	}
	if affected, _ := updated.RowsAffected(); affected != 1 {
		return errors.New("postprocess task lease is no longer valid")
	}
	return nil
}

func (s *PostgresStore) MarkSucceeded(ctx context.Context, task Task) error {
	if err := s.updateLeaseState(ctx, task, StatusSucceeded, "", nil); err != nil {
		return err
	}
	return nil
}

func (s *PostgresStore) Requeue(ctx context.Context, task Task, message string, retryAt time.Time) error {
	return s.updateLeaseState(ctx, task, StatusPending, message, &retryAt)
}

func (s *PostgresStore) MarkDeadLetter(ctx context.Context, task Task, message string) error {
	return s.updateLeaseState(ctx, task, StatusDeadLetter, message, nil)
}

func (s *PostgresStore) updateLeaseState(ctx context.Context, task Task, status Status, message string, retryAt *time.Time) error {
	if task.ID <= 0 || task.LeaseToken == "" {
		return errors.New("invalid postprocess task lease")
	}
	var retryValue any
	if retryAt != nil {
		retryValue = *retryAt
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE document_postprocess_tasks
		SET status = $4, error_message = NULLIF($5, ''),
			completed_at = CASE WHEN $4 IN ('succeeded', 'dead_letter') THEN CURRENT_TIMESTAMP ELSE NULL END,
			next_attempt_at = COALESCE($6, CURRENT_TIMESTAMP), lease_token = NULL, lease_until = NULL
		WHERE id = $1 AND knowledge_base_id = $2 AND status = 'processing' AND lease_token = $3`, task.ID, task.KnowledgeBaseID, task.LeaseToken, status, message, retryValue)
	if err != nil {
		return fmt.Errorf("update postprocess task status: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return errors.New("postprocess task lease is no longer valid")
	}
	return nil
}

func (s *PostgresStore) ListByDocument(ctx context.Context, knowledgeBaseID, documentID int64) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT task.id, task.task_key, task.knowledge_base_id, COALESCE(task.document_id, 0),
			COALESCE(task.asset_id, 0), task.asset_index, COALESCE(task.chunk_position, 0), task.task_kind, task.status,
			task.attempt_count, task.result_text, task.error_message, task.model_provider, task.model_name,
			task.input_tokens, task.output_tokens, task.cost_micros, task.duration_ms,
			task.started_at, task.completed_at, task.updated_at
		FROM document_postprocess_tasks AS task
		JOIN knowledge_bases AS kb ON kb.id = task.knowledge_base_id
		WHERE task.knowledge_base_id = $1 AND task.document_id = $2
		  AND kb.administrator_id = (SELECT administrator_id FROM system_settings WHERE id = 1)
		ORDER BY task.created_at, task.id`, knowledgeBaseID, documentID)
	if err != nil {
		return nil, fmt.Errorf("list document postprocess tasks: %w", err)
	}
	defer rows.Close()
	tasks := make([]Task, 0)
	for rows.Next() {
		var task Task
		var errorMessage, modelProvider, modelName sql.NullString
		var startedAt, completedAt sql.NullTime
		if err := rows.Scan(&task.ID, &task.TaskKey, &task.KnowledgeBaseID, &task.DocumentID, &task.AssetID, &task.AssetIndex, &task.ChunkPosition, &task.Kind, &task.Status, &task.AttemptCount, &task.ResultText, &errorMessage, &modelProvider, &modelName, &task.InputTokens, &task.OutputTokens, &task.CostMicros, &task.DurationMS, &startedAt, &completedAt, &task.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan document postprocess task: %w", err)
		}
		if errorMessage.Valid {
			task.ErrorMessage = errorMessage.String
		}
		if modelProvider.Valid {
			task.ModelProvider = modelProvider.String
		}
		if modelName.Valid {
			task.ModelName = modelName.String
		}
		if startedAt.Valid {
			task.StartedAt = startedAt.Time
		}
		if completedAt.Valid {
			task.CompletedAt = completedAt.Time
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate document postprocess tasks: %w", err)
	}
	return tasks, nil
}

func newLeaseToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate postprocess lease token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
