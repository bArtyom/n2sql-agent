package tcc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	ErrInvalidRequest      = errors.New("invalid tcc request")
	ErrTransactionNotFound = errors.New("tcc transaction not found")
	ErrRecoveryRequired    = errors.New("tcc transaction requires recovery")
	ErrAlreadyFinalized    = errors.New("tcc transaction is already finalized")
)

type State string

const (
	StateTrying     State = "trying"
	StateTried      State = "tried"
	StateConfirming State = "confirming"
	StateConfirmed  State = "confirmed"
	StateCanceling  State = "canceling"
	StateCanceled   State = "canceled"
	StateFailed     State = "failed"
)

func (s State) Terminal() bool {
	return s == StateConfirmed || s == StateCanceled || s == StateFailed
}

type Transaction struct {
	ID         string
	AgentRunID int64
	ToolName   string
	Arguments  json.RawMessage
	State      State
	Result     Result
	LastError  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type Branch struct {
	TransactionID string
	OperationID   string
	Participant   string
	Arguments     json.RawMessage
	State         State
	LastError     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Result struct {
	Content   string
	Metadata  map[string]any
	Operation string
}

// Participant is implemented by a side-effecting business operation. The
// operationID is stable across retries and must be used as the downstream
// idempotency key.
type Participant interface {
	Try(context.Context, string) (Result, error)
	Confirm(context.Context, string) error
	Cancel(context.Context, string) error
}

type ParticipantSpec struct {
	OperationID string
	Name        string
	Participant Participant
}

type TransactionRequest struct {
	TransactionID string
	AgentRunID    int64
	ToolName      string
	Arguments     json.RawMessage
}

type Store interface {
	CreateTransaction(context.Context, Transaction) error
	GetTransaction(context.Context, string) (Transaction, error)
	SetTransactionState(context.Context, string, State, string) error
	SetTransactionResult(context.Context, string, Result) error
	CreateBranch(context.Context, Branch) error
	ListBranches(context.Context, string) ([]Branch, error)
	SetBranchState(context.Context, string, string, State, string) error
}

type Coordinator struct{ store Store }

func NewCoordinator(store Store) (*Coordinator, error) {
	if store == nil {
		return nil, ErrInvalidRequest
	}
	return &Coordinator{store: store}, nil
}

// Execute is deliberately synchronous inside one Agent tool call. The
// database state makes the operation recoverable if the Worker crashes after
// Try succeeds but before Confirm returns.
func (c *Coordinator) Execute(ctx context.Context, request TransactionRequest, specs []ParticipantSpec) (Result, error) {
	if c == nil || c.store == nil || ctx == nil || request.TransactionID == "" ||
		request.AgentRunID <= 0 || request.ToolName == "" || !json.Valid(request.Arguments) || len(specs) == 0 {
		return Result{}, ErrInvalidRequest
	}
	if err := validateSpecs(specs); err != nil {
		return Result{}, err
	}
	now := time.Now().UTC()
	transaction := Transaction{ID: request.TransactionID, AgentRunID: request.AgentRunID, ToolName: request.ToolName, Arguments: request.Arguments, State: StateTrying, CreatedAt: now, UpdatedAt: now}
	if err := c.store.CreateTransaction(ctx, transaction); err != nil {
		return Result{}, fmt.Errorf("create tcc transaction: %w", err)
	}
	if existing, err := c.store.GetTransaction(ctx, request.TransactionID); err != nil {
		return Result{}, fmt.Errorf("load tcc transaction: %w", err)
	} else if existing.State == StateConfirmed {
		return existing.Result, nil
	} else if existing.State == StateCanceled || existing.State == StateFailed {
		return Result{}, ErrAlreadyFinalized
	} else if existing.State == StateCanceling {
		return c.Recover(ctx, request.TransactionID, specs)
	}
	for _, spec := range specs {
		if err := c.store.CreateBranch(ctx, Branch{TransactionID: request.TransactionID, OperationID: spec.OperationID, Participant: spec.Name, Arguments: request.Arguments, State: StateTrying, CreatedAt: now, UpdatedAt: now}); err != nil {
			return Result{}, fmt.Errorf("create tcc branch %s: %w", spec.Name, err)
		}
	}
	return c.run(ctx, request.TransactionID, specs)
}

func (c *Coordinator) run(ctx context.Context, transactionID string, specs []ParticipantSpec) (Result, error) {
	branches, err := c.store.ListBranches(ctx, transactionID)
	if err != nil {
		return Result{}, fmt.Errorf("list tcc branches: %w", err)
	}
	byOperation := make(map[string]Branch, len(branches))
	for _, branch := range branches {
		byOperation[branch.OperationID] = branch
	}
	var results []Result
	for _, spec := range specs {
		branch, ok := byOperation[spec.OperationID]
		if !ok {
			return Result{}, ErrTransactionNotFound
		}
		if branch.State == StateConfirmed {
			continue
		}
		if branch.State == StateTried || branch.State == StateConfirming {
			continue
		}
		result, tryErr := spec.Participant.Try(ctx, spec.OperationID)
		if tryErr != nil {
			_ = c.store.SetBranchState(ctx, transactionID, spec.OperationID, StateFailed, tryErr.Error())
			_ = c.store.SetTransactionState(ctx, transactionID, StateCanceling, tryErr.Error())
			c.cancelBranches(ctx, transactionID, branches, specs, tryErr.Error())
			_ = c.store.SetTransactionState(ctx, transactionID, StateCanceled, tryErr.Error())
			return Result{}, fmt.Errorf("tcc try %s: %w", spec.Name, tryErr)
		}
		if err := c.store.SetBranchState(ctx, transactionID, spec.OperationID, StateTried, ""); err != nil {
			return Result{}, fmt.Errorf("save tcc try %s: %w", spec.Name, err)
		}
		results = append(results, result)
	}
	if err := c.store.SetTransactionState(ctx, transactionID, StateConfirming, ""); err != nil {
		return Result{}, fmt.Errorf("mark tcc confirming: %w", err)
	}
	var final Result
	for _, spec := range specs {
		branch := byOperation[spec.OperationID]
		if branch.State == StateConfirmed {
			continue
		}
		if err := c.store.SetBranchState(ctx, transactionID, spec.OperationID, StateConfirming, ""); err != nil {
			return Result{}, fmt.Errorf("mark tcc confirming %s: %w", spec.Name, err)
		}
		if err := spec.Participant.Confirm(ctx, spec.OperationID); err != nil {
			_ = c.store.SetBranchState(ctx, transactionID, spec.OperationID, StateConfirming, err.Error())
			_ = c.store.SetTransactionState(ctx, transactionID, StateConfirming, err.Error())
			return Result{}, fmt.Errorf("tcc confirm %s: %w: %w", spec.Name, err, ErrRecoveryRequired)
		}
		if err := c.store.SetBranchState(ctx, transactionID, spec.OperationID, StateConfirmed, ""); err != nil {
			return Result{}, fmt.Errorf("save tcc confirm %s: %w", spec.Name, err)
		}
	}
	for _, result := range results {
		if result.Content != "" {
			final = result
			break
		}
	}
	if final.Content == "" {
		final.Content = "TCC operation committed"
	}
	final.Operation = transactionID
	if err := c.store.SetTransactionResult(ctx, transactionID, final); err != nil {
		return Result{}, fmt.Errorf("save tcc result: %w", err)
	}
	if err := c.store.SetTransactionState(ctx, transactionID, StateConfirmed, ""); err != nil {
		return Result{}, fmt.Errorf("mark tcc confirmed: %w", err)
	}
	return final, nil
}

// Recover is called by a Worker that reconstructed the participant from the
// persisted tool name and arguments. Confirming operations continue forward;
// operations that never reached a durable Try are canceled conservatively.
func (c *Coordinator) Recover(ctx context.Context, transactionID string, specs []ParticipantSpec) (Result, error) {
	if c == nil || c.store == nil || ctx == nil || transactionID == "" {
		return Result{}, ErrInvalidRequest
	}
	transaction, err := c.store.GetTransaction(ctx, transactionID)
	if err != nil {
		return Result{}, err
	}
	if transaction.State == StateConfirmed {
		return transaction.Result, nil
	}
	if transaction.State == StateCanceled || transaction.State == StateFailed {
		return Result{}, ErrAlreadyFinalized
	}
	branches, err := c.store.ListBranches(ctx, transactionID)
	if err != nil {
		return Result{}, err
	}
	if transaction.State == StateTrying {
		_ = c.store.SetTransactionState(ctx, transactionID, StateCanceling, "recovered before Try completed")
		c.cancelBranches(ctx, transactionID, branches, specs, "recovered before Try completed")
		_ = c.store.SetTransactionState(ctx, transactionID, StateCanceled, "recovered before Try completed")
		return Result{}, ErrRecoveryRequired
	}
	if transaction.State == StateCanceling {
		c.cancelBranches(ctx, transactionID, branches, specs, "recovered cancel")
		_ = c.store.SetTransactionState(ctx, transactionID, StateCanceled, "recovered cancel")
		return Result{}, ErrRecoveryRequired
	}
	return c.run(ctx, transactionID, specs)
}

func (c *Coordinator) cancelBranches(ctx context.Context, transactionID string, branches []Branch, specs []ParticipantSpec, reason string) {
	states := make(map[string]State, len(branches))
	for _, branch := range branches {
		states[branch.OperationID] = branch.State
	}
	for _, spec := range specs {
		if states[spec.OperationID] == StateConfirmed || states[spec.OperationID] == StateCanceled {
			continue
		}
		_ = c.store.SetBranchState(ctx, transactionID, spec.OperationID, StateCanceling, reason)
		if err := spec.Participant.Cancel(ctx, spec.OperationID); err != nil {
			_ = c.store.SetBranchState(ctx, transactionID, spec.OperationID, StateCanceling, err.Error())
			continue
		}
		_ = c.store.SetBranchState(ctx, transactionID, spec.OperationID, StateCanceled, "")
	}
}

func validateSpecs(specs []ParticipantSpec) error {
	seen := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if spec.OperationID == "" || spec.Name == "" || spec.Participant == nil {
			return ErrInvalidRequest
		}
		if _, ok := seen[spec.OperationID]; ok {
			return fmt.Errorf("duplicate tcc operation: %s", spec.OperationID)
		}
		seen[spec.OperationID] = struct{}{}
	}
	return nil
}

// MemoryStore is useful for unit tests and local examples. Production code
// uses PostgresStore below so operation state survives a Worker crash.
type MemoryStore struct {
	mu           sync.Mutex
	transactions map[string]Transaction
	branches     map[string]Branch
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{transactions: map[string]Transaction{}, branches: map[string]Branch{}}
}

func (s *MemoryStore) CreateTransaction(_ context.Context, value Transaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.transactions[value.ID]; ok {
		if existing.State == StateConfirmed {
			return nil
		}
		return nil
	}
	s.transactions[value.ID] = value
	return nil
}

func (s *MemoryStore) GetTransaction(_ context.Context, id string) (Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.transactions[id]
	if !ok {
		return Transaction{}, ErrTransactionNotFound
	}
	return value, nil
}

func (s *MemoryStore) SetTransactionState(_ context.Context, id string, state State, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.transactions[id]
	if !ok {
		return ErrTransactionNotFound
	}
	value.State, value.LastError, value.UpdatedAt = state, message, time.Now().UTC()
	s.transactions[id] = value
	return nil
}

func (s *MemoryStore) SetTransactionResult(_ context.Context, id string, result Result) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.transactions[id]
	if !ok {
		return ErrTransactionNotFound
	}
	value.Result, value.UpdatedAt = result, time.Now().UTC()
	s.transactions[id] = value
	return nil
}

func (s *MemoryStore) CreateBranch(_ context.Context, value Branch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := branchKey(value.TransactionID, value.OperationID)
	if _, ok := s.branches[key]; !ok {
		s.branches[key] = value
	}
	return nil
}

func (s *MemoryStore) ListBranches(_ context.Context, transactionID string) ([]Branch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	branches := make([]Branch, 0)
	for _, branch := range s.branches {
		if branch.TransactionID == transactionID {
			branches = append(branches, branch)
		}
	}
	if len(branches) == 0 {
		return nil, ErrTransactionNotFound
	}
	return branches, nil
}

func (s *MemoryStore) SetBranchState(_ context.Context, transactionID, operationID string, state State, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := branchKey(transactionID, operationID)
	branch, ok := s.branches[key]
	if !ok {
		return ErrTransactionNotFound
	}
	branch.State, branch.LastError, branch.UpdatedAt = state, message, time.Now().UTC()
	s.branches[key] = branch
	return nil
}

func branchKey(transactionID, operationID string) string {
	return transactionID + "\x00" + operationID
}
