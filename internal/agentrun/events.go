package agentrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/bArtyom/n2sql-agent/internal/agentstream"
)

// EventStore persists a bounded copy of transport events. Hub remains the
// low-latency live path; this store is the restart/replay path.
type EventStore interface {
	Append(context.Context, Run, agentstream.Event) error
	List(context.Context, string, int64) ([]agentstream.Event, error)
}

type PostgresEventStore struct{ db *sql.DB }

func NewPostgresEventStore(db *sql.DB) *PostgresEventStore { return &PostgresEventStore{db: db} }

func (s *PostgresEventStore) Append(ctx context.Context, run Run, event agentstream.Event) error {
	if run.ID <= 0 || event.RunID == "" || event.Type == "" {
		return ErrInvalidRun
	}
	payload, err := json.Marshal(event.Data)
	if err != nil {
		return fmt.Errorf("encode agent event: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO agent_run_events (agent_run_id, event_id, event_type, payload, created_at)
		VALUES ($1, $2, $3, $4, COALESCE($5, CURRENT_TIMESTAMP))
		ON CONFLICT (agent_run_id, event_id) DO NOTHING`,
		run.ID, event.ID, event.Type, payload, event.CreatedAt)
	if err != nil {
		return fmt.Errorf("append agent event: %w", err)
	}
	return nil
}

func (s *PostgresEventStore) List(ctx context.Context, runID string, knowledgeBaseID int64) ([]agentstream.Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.event_id, e.event_type, e.payload, e.created_at
		FROM agent_run_events e
		JOIN agent_runs r ON r.id = e.agent_run_id
		WHERE r.run_id = $1 AND r.knowledge_base_id = $2
		ORDER BY e.id`, runID, knowledgeBaseID)
	if err != nil {
		return nil, fmt.Errorf("list agent events: %w", err)
	}
	defer rows.Close()
	var events []agentstream.Event
	for rows.Next() {
		var event agentstream.Event
		var payload []byte
		if err := rows.Scan(&event.ID, &event.Type, &payload, &event.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan agent event: %w", err)
		}
		if len(payload) > 0 && string(payload) != "null" {
			if err := json.Unmarshal(payload, &event.Data); err != nil {
				return nil, fmt.Errorf("decode agent event: %w", err)
			}
		}
		event.RunID = runID
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent events: %w", err)
	}
	return events, nil
}
