package agentrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/bArtyom/n2sql-agent/internal/agentstream"
)

// EventStore persists the durable Agent event journal. The Hub/Redis stream
// remains the low-latency transport path; this store is the restart/replay and
// audit path.
type EventStore interface {
	Append(context.Context, Run, agentstream.Event) error
	List(context.Context, string, int64) ([]agentstream.Event, error)
}

type PostgresEventStore struct{ db *sql.DB }

func NewPostgresEventStore(db *sql.DB) *PostgresEventStore { return &PostgresEventStore{db: db} }

func eventStreamRunID(run Run, event agentstream.Event) string {
	if event.RunID != "" {
		return event.RunID
	}
	return run.RunID
}

func (s *PostgresEventStore) Append(ctx context.Context, run Run, event agentstream.Event) error {
	if run.ID <= 0 || event.RunID == "" || event.Type == "" {
		return ErrInvalidRun
	}
	payload, err := json.Marshal(struct {
		Data        any    `json:"data"`
		ToolCallID  string `json:"tool_call_id,omitempty"`
		ExecutionID string `json:"execution_id,omitempty"`
		TraceID     string `json:"trace_id,omitempty"`
	}{Data: event.Data, ToolCallID: event.ToolCallID, ExecutionID: event.ExecutionID, TraceID: event.TraceID})
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
		WITH RECURSIVE run_tree AS (
			SELECT id
			FROM agent_runs
			WHERE run_id = $1 AND knowledge_base_id = $2
			UNION ALL
			SELECT child.id
			FROM agent_runs child
			JOIN run_tree parent ON child.parent_run_id = parent.id
			WHERE child.knowledge_base_id = $2
		)
		SELECT e.event_id, e.event_type, e.payload, e.created_at
		FROM agent_run_events e
		JOIN run_tree r ON r.id = e.agent_run_id
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
			var envelope struct {
				Data        json.RawMessage `json:"data"`
				ToolCallID  string          `json:"tool_call_id"`
				ExecutionID string          `json:"execution_id"`
				TraceID     string          `json:"trace_id"`
			}
			if err := json.Unmarshal(payload, &envelope); err != nil {
				return nil, fmt.Errorf("decode agent event: %w", err)
			}
			if len(envelope.Data) == 0 {
				// Read payloads written before the identity envelope was added.
				if err := json.Unmarshal(payload, &event.Data); err != nil {
					return nil, fmt.Errorf("decode legacy agent event: %w", err)
				}
			} else {
				if err := json.Unmarshal(envelope.Data, &event.Data); err != nil {
					return nil, fmt.Errorf("decode agent event data: %w", err)
				}
				event.ToolCallID = envelope.ToolCallID
				event.ExecutionID = envelope.ExecutionID
				event.TraceID = envelope.TraceID
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
