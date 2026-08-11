package conversation

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresStoreSerializesConversationLocksAcrossConnections(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	store := NewPostgresStore(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	firstAcquired := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- store.WithConversationLock(ctx, 987654321, func() error {
			close(firstAcquired)
			<-releaseFirst
			return nil
		})
	}()
	select {
	case <-firstAcquired:
	case err := <-firstDone:
		t.Fatalf("first connection failed before acquiring lock: %v", err)
	case <-ctx.Done():
		t.Fatalf("first connection did not acquire lock: %v", ctx.Err())
	}

	secondAcquired := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- store.WithConversationLock(ctx, 987654321, func() error {
			close(secondAcquired)
			return nil
		})
	}()
	select {
	case <-secondAcquired:
		t.Fatal("second connection acquired the conversation lock before first released")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first lock callback error: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second lock callback error: %v", err)
	}
	select {
	case <-secondAcquired:
	case <-ctx.Done():
		t.Fatalf("second connection did not acquire lock after release: %v", ctx.Err())
	}
}
