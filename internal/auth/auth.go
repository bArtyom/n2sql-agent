package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	SessionCookieName = "n2sql_session"
	SessionLifetime   = 30 * 24 * time.Hour
	MinPasswordBytes  = 8
	MaxEmailBytes     = 320
)

var (
	ErrInvalidEmail    = errors.New("invalid email")
	ErrInvalidPassword = errors.New("invalid password")
	ErrInvalidSession  = errors.New("invalid session")
	ErrEmailExists     = errors.New("email already exists")
	ErrUnauthorized    = errors.New("unauthorized")
	ErrNotFound        = errors.New("user not found")
)

type User struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"createdAt"`
}

type Store interface {
	Register(context.Context, string, string) (User, error)
	Authenticate(context.Context, string, string) (User, string, error)
	UserBySession(context.Context, string) (User, error)
	RevokeSession(context.Context, string) error
}

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }

func (s *PostgresStore) Register(ctx context.Context, email, password string) (User, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return User{}, err
	}
	if err := validatePassword(password); err != nil {
		return User{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, fmt.Errorf("hash password: %w", err)
	}
	var user User
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO app_users (email, password_hash)
		VALUES ($1, $2)
		RETURNING id, email, created_at`, email, string(hash)).Scan(&user.ID, &user.Email, &user.CreatedAt)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate key") {
			return User{}, ErrEmailExists
		}
		return User{}, fmt.Errorf("create user: %w", err)
	}
	return user, nil
}

func (s *PostgresStore) Authenticate(ctx context.Context, email, password string) (User, string, error) {
	email, err := normalizeEmail(email)
	if err != nil || validatePassword(password) != nil {
		return User{}, "", ErrUnauthorized
	}
	var user User
	var passwordHash string
	err = s.db.QueryRowContext(ctx, `
		SELECT id, email, password_hash, created_at
		FROM app_users WHERE lower(email) = lower($1)`, email).Scan(&user.ID, &user.Email, &passwordHash, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) || bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)) != nil {
		return User{}, "", ErrUnauthorized
	}
	token, tokenHash, err := newSessionToken()
	if err != nil {
		return User{}, "", fmt.Errorf("create session token: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO app_sessions (user_id, token_hash, expires_at)
		VALUES ($1, $2, $3)`, user.ID, tokenHash, time.Now().UTC().Add(SessionLifetime))
	if err != nil {
		return User{}, "", fmt.Errorf("save session: %w", err)
	}
	return user, token, nil
}

func (s *PostgresStore) UserBySession(ctx context.Context, token string) (User, error) {
	if strings.TrimSpace(token) == "" {
		return User{}, ErrInvalidSession
	}
	var user User
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.email, u.created_at
		FROM app_sessions AS session
		JOIN app_users AS u ON u.id = session.user_id
		WHERE session.token_hash = $1
		  AND session.revoked_at IS NULL
		  AND session.expires_at > CURRENT_TIMESTAMP`, hashToken(token)).Scan(&user.ID, &user.Email, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUnauthorized
	}
	if err != nil {
		return User{}, fmt.Errorf("load session user: %w", err)
	}
	return user, nil
}

func (s *PostgresStore) RevokeSession(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `UPDATE app_sessions SET revoked_at = CURRENT_TIMESTAMP WHERE token_hash = $1 AND revoked_at IS NULL`, hashToken(token))
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

func normalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if len([]byte(email)) == 0 || len([]byte(email)) > MaxEmailBytes || !strings.Contains(email, "@") || strings.ContainsAny(email, "\r\n") {
		return "", ErrInvalidEmail
	}
	return email, nil
}

func validatePassword(password string) error {
	if len([]byte(password)) < MinPasswordBytes || len([]byte(password)) > 256 || strings.ContainsAny(password, "\r\n") {
		return ErrInvalidPassword
	}
	return nil
}

func newSessionToken() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	token := hex.EncodeToString(raw)
	return token, hashToken(token), nil
}

func hashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

type contextKey struct{}

func WithUser(ctx context.Context, user User) context.Context {
	return context.WithValue(ctx, contextKey{}, user)
}

func UserFromContext(ctx context.Context) (User, bool) {
	user, ok := ctx.Value(contextKey{}).(User)
	return user, ok
}

func Middleware(store Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(SessionCookieName)
			if err == nil && strings.TrimSpace(cookie.Value) != "" {
				if user, lookupErr := store.UserBySession(r.Context(), cookie.Value); lookupErr == nil {
					r = r.WithContext(WithUser(r.Context(), user))
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func Cookie(token string, secure bool) *http.Cookie {
	return &http.Cookie{Name: SessionCookieName, Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode, MaxAge: int(SessionLifetime / time.Second)}
}

func ClearCookie() *http.Cookie {
	return &http.Cookie{Name: SessionCookieName, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1}
}
