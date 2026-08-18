package agentrun

import (
	"testing"
	"time"
)

func TestNewRedisEventStoreRejectsInvalidConfiguration(t *testing.T) {
	if _, err := NewRedisEventStore("", "", time.Hour, 100); err == nil {
		t.Fatal("empty Redis URL should be rejected")
	}
	if _, err := NewRedisEventStore("redis://localhost:6379", "", 0, 100); err == nil {
		t.Fatal("non-positive TTL should be rejected")
	}
	if _, err := NewRedisEventStore("redis://localhost:6379", "", time.Hour, 0); err == nil {
		t.Fatal("non-positive max length should be rejected")
	}
}
