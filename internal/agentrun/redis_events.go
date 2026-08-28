package agentrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/agent"
	"github.com/bArtyom/n2sql-agent/internal/agentstream"
	"github.com/redis/go-redis/v9"
)

const defaultRedisEventPrefix = "n2sql:agent:events"

// LiveEventStore is the short-lived transport stream used by SSE. Unlike the
// PostgreSQL EventStore, it can keep a subscriber attached after a process
// restart or when the request and worker run on different instances.
type LiveEventStore interface {
	EventStore
	Subscribe(context.Context, string, int64) ([]agentstream.Event, <-chan agentstream.Event, func(), bool, error)
	SubscribeFrom(context.Context, string, int64, string) ([]agentstream.Event, <-chan agentstream.Event, func(), bool, error)
}

type LiveEventCleaner interface {
	Delete(context.Context, string, int64) error
}

// RedisEventStore keeps a bounded, expiring copy of Agent transport events.
// The final answer and run state remain in PostgreSQL; Redis is only a replay
// window for the live conversation.
type RedisEventStore struct {
	client *redis.Client
	prefix string
	ttl    time.Duration
	maxLen int64
}

// Publish implements StreamBridge. Redis is selected as the single live
// transport when the server runs in multi-process mode; durable persistence is
// still handled separately by PostgresEventStore.
func (s *RedisEventStore) Publish(ctx context.Context, run Run, event agentstream.Event) error {
	return s.Append(ctx, run, event)
}

func NewRedisEventStore(redisURL, prefix string, ttl time.Duration, maxLen int) (*RedisEventStore, error) {
	if redisURL == "" || ttl <= 0 || maxLen <= 0 {
		return nil, errors.New("invalid redis event stream configuration")
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse agent stream redis URL: %w", err)
	}
	client := redis.NewClient(options)
	if err := client.Ping(context.Background()).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("connect agent stream redis: %w", err)
	}
	if prefix == "" {
		prefix = defaultRedisEventPrefix
	}
	return &RedisEventStore{client: client, prefix: prefix, ttl: ttl, maxLen: int64(maxLen)}, nil
}

func (s *RedisEventStore) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

func (s *RedisEventStore) key(runID string, knowledgeBaseID int64) string {
	return fmt.Sprintf("%s:%d:%s", s.prefix, knowledgeBaseID, runID)
}

func (s *RedisEventStore) Append(ctx context.Context, run Run, event agentstream.Event) error {
	if s == nil || s.client == nil || run.RunID == "" || run.KnowledgeBaseID <= 0 || event.ID == "" || event.Type == "" {
		return ErrInvalidRun
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode agent stream event: %w", err)
	}
	key := s.key(eventStreamRunID(run, event), run.KnowledgeBaseID)
	pipe := s.client.TxPipeline()
	pipe.XAdd(ctx, &redis.XAddArgs{
		Stream: key,
		MaxLen: s.maxLen,
		Approx: true,
		Values: map[string]any{"event": payload},
	})
	pipe.Expire(ctx, key, s.ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("append agent stream event: %w", err)
	}
	return nil
}

func (s *RedisEventStore) List(ctx context.Context, runID string, knowledgeBaseID int64) ([]agentstream.Event, error) {
	if s == nil || s.client == nil || runID == "" || knowledgeBaseID <= 0 {
		return nil, ErrInvalidRun
	}
	entries, err := s.client.XRange(ctx, s.key(runID, knowledgeBaseID), "-", "+").Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("list agent stream events: %w", err)
	}
	return decodeRedisEvents(entries, runID), nil
}

func (s *RedisEventStore) Delete(ctx context.Context, runID string, knowledgeBaseID int64) error {
	if s == nil || s.client == nil || runID == "" || knowledgeBaseID <= 0 {
		return ErrInvalidRun
	}
	if err := s.client.Del(ctx, s.key(runID, knowledgeBaseID)).Err(); err != nil {
		return fmt.Errorf("delete agent stream events: %w", err)
	}
	return nil
}

// Subscribe replays the retained window and then tails new events. A complete
// event closes the live channel; the run status/final response remains the
// durable source of truth after the Redis TTL expires.
func (s *RedisEventStore) Subscribe(ctx context.Context, runID string, knowledgeBaseID int64) ([]agentstream.Event, <-chan agentstream.Event, func(), bool, error) {
	return s.SubscribeFrom(ctx, runID, knowledgeBaseID, "")
}

func (s *RedisEventStore) SubscribeFrom(ctx context.Context, runID string, knowledgeBaseID int64, afterEventID string) ([]agentstream.Event, <-chan agentstream.Event, func(), bool, error) {
	if s == nil || s.client == nil || runID == "" || knowledgeBaseID <= 0 {
		return nil, nil, nil, false, ErrInvalidRun
	}
	key := s.key(runID, knowledgeBaseID)
	entries, err := s.client.XRange(ctx, key, "-", "+").Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, nil, nil, false, fmt.Errorf("replay agent stream events: %w", err)
	}
	snapshot := decodeRedisEvents(entries, runID)
	lastID := "0-0"
	cursorTerminal := false
	if afterEventID != "" {
		cursorIndex := -1
		for index, entry := range entries {
			event, ok := decodeRedisEvent(entry, runID)
			if ok && event.ID == afterEventID {
				cursorIndex = index
				cursorTerminal = isTerminalAgentEvent(event.Type)
				break
			}
		}
		if cursorIndex < 0 {
			return nil, nil, nil, false, agentstream.NewEventGap(afterEventID, snapshot)
		}
		lastID = entries[cursorIndex].ID
	} else if len(entries) > 0 {
		lastID = entries[len(entries)-1].ID
	}
	if afterEventID != "" {
		filtered := make([]agentstream.Event, 0, len(snapshot))
		found := false
		for _, event := range snapshot {
			if found {
				filtered = append(filtered, event)
			}
			if event.ID == afterEventID {
				found = true
			}
		}
		snapshot = filtered
	}
	if cursorTerminal {
		return snapshot, closedEventChannel(), func() {}, true, nil
	}
	for _, event := range snapshot {
		if event.Type == string(agent.EventRunFinished) || event.Type == string(agent.EventRunFailed) || event.Type == string(agent.EventRunCanceled) {
			return snapshot, closedEventChannel(), func() {}, true, nil
		}
	}

	live := make(chan agentstream.Event, 128)
	streamCtx, cancel := context.WithCancel(ctx)
	var once sync.Once
	stop := func() { once.Do(cancel) }
	go func() {
		defer close(live)
		defer cancel()
		for {
			result, err := s.client.XRead(streamCtx, &redis.XReadArgs{
				Streams: []string{key, lastID},
				Count:   128,
				Block:   5 * time.Second,
			}).Result()
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, redis.Nil) {
					return
				}
				return
			}
			for _, stream := range result {
				for _, entry := range stream.Messages {
					lastID = entry.ID
					event, ok := decodeRedisEvent(entry, runID)
					if !ok {
						continue
					}
					select {
					case live <- event:
					case <-streamCtx.Done():
						return
					}
					if event.Type == string(agent.EventRunFinished) || event.Type == string(agent.EventRunFailed) || event.Type == string(agent.EventRunCanceled) {
						return
					}
				}
			}
		}
	}()
	return snapshot, live, stop, false, nil
}

func isTerminalAgentEvent(eventType string) bool {
	return eventType == string(agent.EventRunFinished) || eventType == string(agent.EventRunFailed) || eventType == string(agent.EventRunCanceled)
}

func decodeRedisEvents(entries []redis.XMessage, runID string) []agentstream.Event {
	events := make([]agentstream.Event, 0, len(entries))
	for _, entry := range entries {
		if event, ok := decodeRedisEvent(entry, runID); ok {
			events = append(events, event)
		}
	}
	return events
}

func decodeRedisEvent(entry redis.XMessage, runID string) (agentstream.Event, bool) {
	value, ok := entry.Values["event"].(string)
	if !ok {
		if bytes, ok := entry.Values["event"].([]byte); ok {
			value = string(bytes)
		} else {
			return agentstream.Event{}, false
		}
	}
	var event agentstream.Event
	if json.Unmarshal([]byte(value), &event) != nil || event.ID == "" || event.Type == "" {
		return agentstream.Event{}, false
	}
	if event.Version == 0 {
		event.Version = agentstream.EventSchemaVersion
	}
	if event.Version != agentstream.EventSchemaVersion {
		return agentstream.Event{}, false
	}
	event.RunID = runID
	return event, true
}

func closedEventChannel() <-chan agentstream.Event {
	channel := make(chan agentstream.Event)
	close(channel)
	return channel
}

var _ LiveEventStore = (*RedisEventStore)(nil)
var _ StreamBridge = (*RedisEventStore)(nil)
