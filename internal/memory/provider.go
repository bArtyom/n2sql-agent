package memory

import (
	"context"
	"errors"
	"strings"
)

var (
	ErrInvalidScope      = errors.New("invalid memory scope")
	ErrUnsupportedUpdate = errors.New("memory provider does not support update")
)

// Scope makes user and knowledge-base ownership explicit at the memory
// boundary. Every Provider operation must carry both dimensions.
type Scope struct {
	UserID          int64
	KnowledgeBaseID int64
}

// Context is the bounded prompt projection returned to an Agent. The storage
// provider may retain more memories, but only this projection is injected.
type Context struct {
	Profile  Profile
	Memories []Memory
}

// Provider is the DeerFlow-style memory seam. Storage, indexing, and future
// Mem0/DeerMem adapters can change without changing the Agent service.
type Provider interface {
	Add(context.Context, Scope, CreateInput) (Memory, error)
	GetContext(context.Context, Scope, int) (Context, error)
	Search(context.Context, Scope, string, int) ([]Memory, error)
	Update(context.Context, Scope, int64, string) (Memory, error)
	Delete(context.Context, Scope, int64) error
}

type MemoryUpdater interface {
	Update(context.Context, int64, int64, int64, string) (Memory, error)
}

// StoreProvider adapts the current PostgreSQL-oriented Store/ProfileStore to
// the provider contract. Search is deliberately a replaceable baseline; a
// vector/FTS-backed provider can implement the same interface later.
type StoreProvider struct {
	store   Store
	profile ProfileStore
}

func NewStoreProvider(store Store, profile ProfileStore) *StoreProvider {
	return &StoreProvider{store: store, profile: profile}
}

func (p *StoreProvider) Add(ctx context.Context, scope Scope, input CreateInput) (Memory, error) {
	if err := validateScope(scope); err != nil {
		return Memory{}, err
	}
	if p == nil || p.store == nil {
		return Memory{}, ErrInvalidMemory
	}
	input.KnowledgeBaseID = scope.KnowledgeBaseID
	return p.store.Create(ctx, scope.UserID, input)
}

func (p *StoreProvider) GetContext(ctx context.Context, scope Scope, limit int) (Context, error) {
	if err := validateScope(scope); err != nil {
		return Context{}, err
	}
	if p == nil || p.store == nil {
		return Context{}, ErrInvalidMemory
	}
	result := Context{}
	if p.profile != nil {
		profile, err := p.profile.GetProfile(ctx, scope.UserID)
		if err != nil {
			return Context{}, err
		}
		result.Profile = profile
	}
	items, err := p.store.List(ctx, scope.UserID, scope.KnowledgeBaseID)
	if err != nil {
		return Context{}, err
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	result.Memories = items
	return result, nil
}

func (p *StoreProvider) Search(ctx context.Context, scope Scope, query string, limit int) ([]Memory, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	items, err := p.GetContext(ctx, scope, 0)
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		if limit > 0 && len(items.Memories) > limit {
			return items.Memories[:limit], nil
		}
		return items.Memories, nil
	}
	matched := make([]Memory, 0)
	for _, item := range items.Memories {
		if strings.Contains(strings.ToLower(item.Content), query) {
			matched = append(matched, item)
			if limit > 0 && len(matched) == limit {
				break
			}
		}
	}
	return matched, nil
}

func (p *StoreProvider) Update(ctx context.Context, scope Scope, memoryID int64, content string) (Memory, error) {
	if err := validateScope(scope); err != nil {
		return Memory{}, err
	}
	if p == nil || p.store == nil {
		return Memory{}, ErrInvalidMemory
	}
	updater, ok := any(p.store).(MemoryUpdater)
	if !ok {
		return Memory{}, ErrUnsupportedUpdate
	}
	return updater.Update(ctx, scope.UserID, scope.KnowledgeBaseID, memoryID, content)
}

func (p *StoreProvider) Delete(ctx context.Context, scope Scope, memoryID int64) error {
	if err := validateScope(scope); err != nil {
		return err
	}
	if p == nil || p.store == nil {
		return ErrInvalidMemory
	}
	return p.store.Delete(ctx, scope.UserID, scope.KnowledgeBaseID, memoryID)
}

func validateScope(scope Scope) error {
	if scope.UserID <= 0 || scope.KnowledgeBaseID <= 0 {
		return ErrInvalidScope
	}
	return nil
}
