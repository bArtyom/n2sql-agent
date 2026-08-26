package tcc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// PostgresStore persists the TCC state machine. Closures and credentials are
// never stored; a recovery Worker reconstructs the participant from ToolName
// and the bounded Arguments snapshot.
type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, ErrInvalidRequest
	}
	return &PostgresStore{db: db}, nil
}

func (s *PostgresStore) CreateTransaction(ctx context.Context, value Transaction) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_tcc_transactions
			(transaction_id, agent_run_id, tool_name, arguments, state, last_error, created_at, updated_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, NULLIF($6, ''), $7, $8)
		ON CONFLICT (transaction_id) DO NOTHING
	`, value.ID, value.AgentRunID, value.ToolName, []byte(value.Arguments), value.State, value.LastError, value.CreatedAt, value.UpdatedAt)
	return err
}

func (s *PostgresStore) GetTransaction(ctx context.Context, id string) (Transaction, error) {
	var value Transaction
	var args, result []byte
	var lastError sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT transaction_id, agent_run_id, tool_name, arguments, state, result, last_error, created_at, updated_at
		FROM agent_tcc_transactions WHERE transaction_id = $1
	`, id).Scan(&value.ID, &value.AgentRunID, &value.ToolName, &args, &value.State, &result, &lastError, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Transaction{}, ErrTransactionNotFound
	}
	if err != nil {
		return Transaction{}, err
	}
	value.Arguments = json.RawMessage(args)
	value.LastError = lastError.String
	if len(result) > 0 {
		if err := json.Unmarshal(result, &value.Result); err != nil {
			return Transaction{}, err
		}
	}
	return value, nil
}

func (s *PostgresStore) SetTransactionState(ctx context.Context, id string, state State, message string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE agent_tcc_transactions
		SET state = $2, last_error = NULLIF($3, ''), updated_at = CURRENT_TIMESTAMP
		WHERE transaction_id = $1
	`, id, state, message)
	if err != nil {
		return err
	}
	return requireRows(result, ErrTransactionNotFound)
}

func (s *PostgresStore) SetTransactionResult(ctx context.Context, id string, value Result) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE agent_tcc_transactions SET result = $2::jsonb, updated_at = CURRENT_TIMESTAMP
		WHERE transaction_id = $1
	`, id, payload)
	if err != nil {
		return err
	}
	return requireRows(result, ErrTransactionNotFound)
}

func (s *PostgresStore) CreateBranch(ctx context.Context, value Branch) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_tcc_branches
			(transaction_id, operation_id, participant, arguments, state, last_error, created_at, updated_at)
		VALUES ($1, $2, $3, $4::jsonb, $5, NULLIF($6, ''), $7, $8)
		ON CONFLICT (transaction_id, operation_id) DO NOTHING
	`, value.TransactionID, value.OperationID, value.Participant, []byte(value.Arguments), value.State, value.LastError, value.CreatedAt, value.UpdatedAt)
	return err
}

func (s *PostgresStore) ListBranches(ctx context.Context, transactionID string) ([]Branch, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT transaction_id, operation_id, participant, arguments, state, last_error, created_at, updated_at
		FROM agent_tcc_branches WHERE transaction_id = $1 ORDER BY created_at, operation_id
	`, transactionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	branches := make([]Branch, 0)
	for rows.Next() {
		var branch Branch
		var args []byte
		var lastError sql.NullString
		if err := rows.Scan(&branch.TransactionID, &branch.OperationID, &branch.Participant, &args, &branch.State, &lastError, &branch.CreatedAt, &branch.UpdatedAt); err != nil {
			return nil, err
		}
		branch.Arguments = json.RawMessage(args)
		branch.LastError = lastError.String
		branches = append(branches, branch)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(branches) == 0 {
		return nil, ErrTransactionNotFound
	}
	return branches, nil
}

func (s *PostgresStore) SetBranchState(ctx context.Context, transactionID, operationID string, state State, message string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE agent_tcc_branches
		SET state = $3, last_error = NULLIF($4, ''), updated_at = CURRENT_TIMESTAMP
		WHERE transaction_id = $1 AND operation_id = $2
	`, transactionID, operationID, state, message)
	if err != nil {
		return err
	}
	return requireRows(result, ErrTransactionNotFound)
}

func requireRows(result sql.Result, missing error) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return missing
	}
	return nil
}

var _ Store = (*PostgresStore)(nil)
