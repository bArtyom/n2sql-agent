package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/bArtyom/n2sql-agent/internal/memory"
)

type providerStoreStub struct {
	items   []memory.Memory
	profile memory.Profile
}

func (s *providerStoreStub) Create(_ context.Context, userID int64, input memory.CreateInput) (memory.Memory, error) {
	item := memory.Memory{ID: int64(len(s.items) + 1), UserID: userID, KnowledgeBaseID: input.KnowledgeBaseID, Content: input.Content, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	s.items = append(s.items, item)
	return item, nil
}
func (s *providerStoreStub) List(context.Context, int64, int64) ([]memory.Memory, error) {
	return append([]memory.Memory(nil), s.items...), nil
}
func (s *providerStoreStub) Delete(context.Context, int64, int64, int64) error { return nil }
func (s *providerStoreStub) GetProfile(context.Context, int64) (memory.Profile, error) {
	return s.profile, nil
}
func (s *providerStoreStub) SaveProfile(context.Context, int64, string) (memory.Profile, error) {
	return s.profile, nil
}
func (s *providerStoreStub) DeleteProfile(context.Context, int64) error { return nil }

func TestMemoryProviderReturnsBoundedContextAndSearchesScopedMemories(t *testing.T) {
	store := &providerStoreStub{
		items: []memory.Memory{
			{ID: 1, UserID: 7, KnowledgeBaseID: 9, Content: "喜欢 Go"},
			{ID: 2, UserID: 7, KnowledgeBaseID: 9, Content: "喜欢 PostgreSQL"},
		},
		profile: memory.Profile{UserID: 7, Content: "回答要简洁", Version: 2},
	}
	provider := memory.NewStoreProvider(store, store)
	contextValue, err := provider.GetContext(context.Background(), memory.Scope{UserID: 7, KnowledgeBaseID: 9}, 1)
	if err != nil || contextValue.Profile.Content != "回答要简洁" || len(contextValue.Memories) != 1 {
		t.Fatalf("GetContext() = %#v, %v", contextValue, err)
	}
	results, err := provider.Search(context.Background(), memory.Scope{UserID: 7, KnowledgeBaseID: 9}, "PostgreSQL", 5)
	if err != nil || len(results) != 1 || results[0].Content != "喜欢 PostgreSQL" {
		t.Fatalf("Search() = %#v, %v", results, err)
	}
}
