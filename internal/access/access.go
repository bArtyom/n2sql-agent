package access

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/bArtyom/n2sql-agent/internal/auth"
	"github.com/jackc/pgx/v5/pgconn"
)

type Role string

const (
	RoleOwner  Role = "owner"
	RoleEditor Role = "editor"
	RoleViewer Role = "viewer"
)

type Permission string

const (
	PermissionRead  Permission = "read"
	PermissionWrite Permission = "write"
	PermissionAdmin Permission = "admin"
)

var (
	ErrUnauthorized = errors.New("user is not authenticated")
	ErrForbidden    = errors.New("user is not authorized")
	ErrUserNotFound = errors.New("user not found")
	ErrInvalidRole  = errors.New("invalid knowledge base role")
)

type Member struct {
	KnowledgeBaseID int64  `json:"knowledgeBaseId"`
	UserID          int64  `json:"userId"`
	Email           string `json:"email"`
	Role            Role   `json:"role"`
}

type Store interface {
	Authorize(context.Context, int64, int64, Permission) error
	ListMembers(context.Context, int64, int64) ([]Member, error)
	UpsertMember(context.Context, int64, int64, int64, Role) error
	RemoveMember(context.Context, int64, int64, int64) error
}

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) Authorize(ctx context.Context, userID, knowledgeBaseID int64, permission Permission) error {
	if userID <= 0 {
		return ErrUnauthorized
	}
	if knowledgeBaseID <= 0 {
		return ErrForbidden
	}
	var role Role
	err := s.db.QueryRowContext(ctx, `
		SELECT role
		FROM knowledge_base_members
		WHERE knowledge_base_id = $1 AND user_id = $2`, knowledgeBaseID, userID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return fmt.Errorf("authorize knowledge base access: %w", err)
	}
	switch permission {
	case PermissionRead:
		return nil
	case PermissionWrite:
		if role == RoleOwner || role == RoleEditor {
			return nil
		}
	case PermissionAdmin:
		if role == RoleOwner {
			return nil
		}
	default:
		return ErrForbidden
	}
	return ErrForbidden
}

func (s *PostgresStore) ListMembers(ctx context.Context, userID, knowledgeBaseID int64) ([]Member, error) {
	if err := s.Authorize(ctx, userID, knowledgeBaseID, PermissionAdmin); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT member.knowledge_base_id, member.user_id, user.email, member.role
		FROM knowledge_base_members AS member
		JOIN app_users AS user ON user.id = member.user_id
		WHERE member.knowledge_base_id = $1
		ORDER BY member.role, member.user_id`, knowledgeBaseID)
	if err != nil {
		return nil, fmt.Errorf("list knowledge base members: %w", err)
	}
	defer rows.Close()
	items := make([]Member, 0)
	for rows.Next() {
		var member Member
		if err := rows.Scan(&member.KnowledgeBaseID, &member.UserID, &member.Email, &member.Role); err != nil {
			return nil, fmt.Errorf("scan knowledge base member: %w", err)
		}
		items = append(items, member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate knowledge base members: %w", err)
	}
	return items, nil
}

func (s *PostgresStore) UpsertMember(ctx context.Context, actorID, knowledgeBaseID, targetUserID int64, role Role) error {
	if err := validateRole(role); err != nil {
		return err
	}
	if err := s.Authorize(ctx, actorID, knowledgeBaseID, PermissionAdmin); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO knowledge_base_members (knowledge_base_id, user_id, role)
		SELECT $1, id, $4
		FROM app_users
		WHERE id = $2
		ON CONFLICT (knowledge_base_id, user_id)
		DO UPDATE SET role = EXCLUDED.role`, knowledgeBaseID, targetUserID, actorID, role)
	if err != nil {
		return fmt.Errorf("upsert knowledge base member: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("count knowledge base member changes: %w", err)
	} else if affected == 0 {
		return ErrUserNotFound
	}
	return nil
}

func (s *PostgresStore) RemoveMember(ctx context.Context, actorID, knowledgeBaseID, targetUserID int64) error {
	if err := s.Authorize(ctx, actorID, knowledgeBaseID, PermissionAdmin); err != nil {
		return err
	}
	if actorID == targetUserID {
		return ErrForbidden
	}
	var ownerCount int
	var targetRole Role
	if err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM knowledge_base_members WHERE knowledge_base_id = $1 AND role = 'owner'),
			COALESCE((SELECT role FROM knowledge_base_members WHERE knowledge_base_id = $1 AND user_id = $2), '')`, knowledgeBaseID, targetUserID).Scan(&ownerCount, &targetRole); err != nil {
		return fmt.Errorf("check knowledge base member removal: %w", err)
	}
	if targetRole == RoleOwner && ownerCount <= 1 {
		return ErrForbidden
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM knowledge_base_members
		WHERE knowledge_base_id = $1 AND user_id = $2`, knowledgeBaseID, targetUserID)
	if err != nil {
		return fmt.Errorf("remove knowledge base member: %w", err)
	}
	if affected, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("count removed knowledge base member: %w", err)
	} else if affected == 0 {
		return ErrUserNotFound
	}
	return nil
}

func validateRole(role Role) error {
	switch role {
	case RoleOwner, RoleEditor, RoleViewer:
		return nil
	default:
		return ErrInvalidRole
	}
}

// Middleware requires a logged-in user for application APIs and checks the
// membership role before a knowledge-base route reaches its handler. Stores
// still perform their own ownership checks; this middleware is the early,
// uniform boundary that prevents accidental exposure through a new route.
func Middleware(store Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isPublicPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			user, ok := auth.UserFromContext(r.Context())
			if !ok {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			if store == nil {
				writeError(w, http.StatusInternalServerError, "authorization unavailable")
				return
			}
			knowledgeBaseID, hasKnowledgeBase := pathKnowledgeBaseID(r.URL.Path)
			if !hasKnowledgeBase {
				next.ServeHTTP(w, r)
				return
			}
			permission := permissionFor(r.Method, r.URL.Path)
			if err := store.Authorize(r.Context(), user.ID, knowledgeBaseID, permission); err != nil {
				writeAccessError(w, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isPublicPath(path string) bool {
	return path == "/health" || path == "/metrics" || strings.HasPrefix(path, "/api/auth/")
}

func pathKnowledgeBaseID(path string) (int64, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 || parts[0] != "api" || parts[1] != "knowledge-bases" {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[2], 10, 64)
	return id, err == nil && id > 0
}

func permissionFor(method, path string) Permission {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 4 {
		if method == http.MethodDelete {
			return PermissionAdmin
		}
		return PermissionWrite
	}
	resource := parts[3]
	if resource == "members" {
		return PermissionAdmin
	}
	if method == http.MethodGet || method == http.MethodHead {
		return PermissionRead
	}
	if resource == "search" || resource == "chat" || resource == "agent-chat" || resource == "mcp" || resource == "conversations" && method == http.MethodPost {
		return PermissionRead
	}
	if method == http.MethodDelete && len(parts) == 3 {
		return PermissionAdmin
	}
	return PermissionWrite
}

func writeAccessError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrUnauthorized):
		writeError(w, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden")
	default:
		writeError(w, http.StatusInternalServerError, "authorization failed")
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func isForeignKeyError(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == "23503"
}
