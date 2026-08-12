package a2a_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/a2a"
	"github.com/bArtyom/n2sql-agent/internal/multiagent"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func openA2AIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping database: %v", err)
	}
	return db
}

func createIntegrationKnowledgeBase(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var id int64
	name := fmt.Sprintf("a2a integration %d", time.Now().UnixNano())
	if err := db.QueryRowContext(context.Background(), `
		INSERT INTO knowledge_bases (administrator_id, name, description)
		SELECT administrator_id, $1, 'A2A integration test'
		FROM system_settings WHERE id = 1
		RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("create integration knowledge base: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM knowledge_bases WHERE id = $1`, id)
	})
	return id
}

func createIntegrationTask(t *testing.T, store a2a.TaskStore, kbID int64) a2a.Task {
	t.Helper()
	input := a2a.CreateInput{
		ID:              fmt.Sprintf("a2a-integration-%d", time.Now().UnixNano()),
		KnowledgeBaseID: kbID,
		Message:         "集成测试问题",
		TopK:            3,
	}
	task, err := store.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return task
}

func TestPostgresStoreCreatesAndReadsCurrentAdministratorTask(t *testing.T) {
	db := openA2AIntegrationDB(t)
	kbID := createIntegrationKnowledgeBase(t, db)
	store := a2a.NewPostgresStore(db)
	task := createIntegrationTask(t, store, kbID)
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM a2a_tasks WHERE id = $1`, task.ID)
	})

	if task.Status != a2a.StatusSubmitted || task.KnowledgeBaseID != kbID || task.AttemptCount != 0 {
		t.Fatalf("created task = %#v, want submitted task", task)
	}
	loaded, err := store.Get(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if loaded.ID != task.ID || loaded.Message != task.Message || loaded.TopK != 3 {
		t.Fatalf("loaded task = %#v, want persisted task", loaded)
	}
}

func TestPostgresStoreRejectsKnowledgeBaseOwnedByAnotherAdministrator(t *testing.T) {
	db := openA2AIntegrationDB(t)
	var administratorID, knowledgeBaseID int64
	if err := db.QueryRowContext(context.Background(), `
		INSERT INTO administrators (display_name) VALUES ('A2A integration other administrator')
		RETURNING id`).Scan(&administratorID); err != nil {
		t.Fatalf("create other administrator: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM knowledge_bases WHERE id = $1`, knowledgeBaseID)
		_, _ = db.ExecContext(context.Background(), `DELETE FROM administrators WHERE id = $1`, administratorID)
	})
	if err := db.QueryRowContext(context.Background(), `
		INSERT INTO knowledge_bases (administrator_id, name, description)
		VALUES ($1, $2, '') RETURNING id`, administratorID, fmt.Sprintf("other a2a kb %d", time.Now().UnixNano())).Scan(&knowledgeBaseID); err != nil {
		t.Fatalf("create other knowledge base: %v", err)
	}

	_, err := a2a.NewPostgresStore(db).Create(context.Background(), a2a.CreateInput{
		ID:              fmt.Sprintf("a2a-denied-%d", time.Now().UnixNano()),
		KnowledgeBaseID: knowledgeBaseID,
		Message:         "不应创建",
		TopK:            3,
	})
	if !errors.Is(err, a2a.ErrTaskNotFound) {
		t.Fatalf("Create() error = %v, want ErrTaskNotFound", err)
	}
}

func TestPostgresStoreClaimsOnceAndReclaimsExpiredLease(t *testing.T) {
	db := openA2AIntegrationDB(t)
	kbID := createIntegrationKnowledgeBase(t, db)
	store := a2a.NewPostgresStore(db)
	task := createIntegrationTask(t, store, kbID)
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM a2a_tasks WHERE id = $1`, task.ID)
	})

	claimed, err := store.ClaimNext(context.Background(), 10*time.Minute)
	if err != nil {
		t.Fatalf("first ClaimNext() error = %v", err)
	}
	if claimed.ID != task.ID || claimed.Status != a2a.StatusWorking || claimed.AttemptCount != 1 {
		t.Fatalf("first claimed task = %#v, want working attempt 1", claimed)
	}
	if _, err := db.ExecContext(context.Background(), `UPDATE a2a_tasks SET lease_until = CURRENT_TIMESTAMP - INTERVAL '1 second' WHERE id = $1`, task.ID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}

	reclaimed, err := store.ClaimNext(context.Background(), 10*time.Minute)
	if err != nil {
		t.Fatalf("second ClaimNext() error = %v", err)
	}
	if reclaimed.ID != task.ID || reclaimed.AttemptCount != 2 {
		t.Fatalf("reclaimed task = %#v, want attempt 2", reclaimed)
	}
	if err := store.MarkCompleted(context.Background(), task.ID, multiagent.Response{Answer: "已完成"}); err != nil {
		t.Fatalf("MarkCompleted() error = %v", err)
	}
	completed, err := store.Get(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("Get() after completion error = %v", err)
	}
	if completed.Status != a2a.StatusCompleted || completed.Response.Answer != "已完成" {
		t.Fatalf("completed task = %#v, want saved response", completed)
	}
}

func TestPostgresStoreDoesNotClaimTaskTwiceConcurrently(t *testing.T) {
	db := openA2AIntegrationDB(t)
	kbID := createIntegrationKnowledgeBase(t, db)
	firstStore := a2a.NewPostgresStore(db)
	secondStore := a2a.NewPostgresStore(db)
	task := createIntegrationTask(t, firstStore, kbID)
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM a2a_tasks WHERE id = $1`, task.ID)
	})

	start := make(chan struct{})
	type result struct {
		task a2a.Task
		err  error
	}
	results := make(chan result, 2)
	var wait sync.WaitGroup
	for _, store := range []a2a.TaskStore{firstStore, secondStore} {
		wait.Add(1)
		go func(store a2a.TaskStore) {
			defer wait.Done()
			<-start
			claimed, err := store.ClaimNext(context.Background(), time.Minute)
			results <- result{task: claimed, err: err}
		}(store)
	}
	close(start)
	wait.Wait()
	close(results)

	claimedCount := 0
	noTaskCount := 0
	for result := range results {
		switch {
		case result.err == nil && result.task.ID == task.ID:
			claimedCount++
		case errors.Is(result.err, a2a.ErrNoTask):
			noTaskCount++
		default:
			t.Fatalf("ClaimNext() result = %#v, want one claim and one ErrNoTask", result)
		}
	}
	if claimedCount != 1 || noTaskCount != 1 {
		t.Fatalf("claimedCount=%d noTaskCount=%d, want 1 and 1", claimedCount, noTaskCount)
	}
}
