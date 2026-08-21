package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"fri.local/football-reputation-index/internal/domain"
)

const (
	// Sessions last a month. Long enough that a returning visitor stays
	// logged in between match weeks, short enough that a stolen cookie
	// expires on its own.
	sessionTTL = 30 * 24 * time.Hour
	// Minimum password length. Deliberately modest: this guards a football
	// leaderboard, and a longer rule mostly pushes people toward reuse.
	minPasswordLength = 8
	// bcrypt's default cost (10) is ~50ms per hash. Enough to make offline
	// cracking expensive without turning login into a visible pause.
	bcryptCost = bcrypt.DefaultCost
)

// ErrInvalidCredentials covers both "no such account" and "wrong password".
// One error for both is the point: distinguishing them tells an attacker
// which addresses are registered.
var ErrInvalidCredentials = errors.New("invalid email or password")

// ErrWeakPassword and ErrInvalidEmail are user-correctable input problems.
var (
	ErrWeakPassword = fmt.Errorf("password must be at least %d characters", minPasswordLength)
	ErrInvalidEmail = errors.New("invalid email address")
)

type authStore interface {
	CreateUser(ctx context.Context, email, passwordHash string, isAdmin bool) (domain.User, error)
	GetUserByEmail(ctx context.Context, email string) (domain.User, error)
	TouchUserLogin(ctx context.Context, userID int64) error
	CreateSession(ctx context.Context, token string, userID int64, expiresAt time.Time) error
	UserBySessionToken(ctx context.Context, token string) (domain.User, error)
	DeleteSession(ctx context.Context, token string) error
	CountUsers(ctx context.Context) (int64, error)
}

// adminEmails are the accounts that get is_admin on registration. Admin is
// granted by owning the project's own address rather than by a flag someone
// can pass in, so registering can never escalate anyone by accident.
var adminEmails = map[string]bool{
	"fri.index@gmail.com": true,
}

func validateCredentials(email, password string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", ErrInvalidEmail
	}
	// net/mail is stricter than a regex and it is in the standard library.
	if _, err := mail.ParseAddress(email); err != nil {
		return "", ErrInvalidEmail
	}
	if len([]rune(password)) < minPasswordLength {
		return "", ErrWeakPassword
	}
	return email, nil
}

// newSessionToken returns 32 bytes of crypto/rand as URL-safe base64. Used
// directly as the cookie value, so it must be unguessable.
func newSessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Register creates an account and opens a session for it in one step, so a
// new visitor lands logged in rather than on a login form.
func (s *Service) Register(ctx context.Context, email, password string) (domain.User, string, time.Time, error) {
	normalized, err := validateCredentials(email, password)
	if err != nil {
		return domain.User{}, "", time.Time{}, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return domain.User{}, "", time.Time{}, err
	}

	user, err := s.auth.CreateUser(ctx, normalized, string(hash), adminEmails[normalized])
	if err != nil {
		return domain.User{}, "", time.Time{}, err
	}

	token, expiresAt, err := s.openSession(ctx, user.ID)
	if err != nil {
		return domain.User{}, "", time.Time{}, err
	}
	return user, token, expiresAt, nil
}

// Login verifies a password and opens a session.
func (s *Service) Login(ctx context.Context, email, password string) (domain.User, string, time.Time, error) {
	user, err := s.auth.GetUserByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
	if err != nil {
		// Spend a hash anyway. Returning early on an unknown address makes
		// the response measurably faster than a wrong password, which turns
		// timing into an account-enumeration oracle.
		_, _ = bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
		return domain.User{}, "", time.Time{}, ErrInvalidCredentials
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return domain.User{}, "", time.Time{}, ErrInvalidCredentials
	}

	token, expiresAt, err := s.openSession(ctx, user.ID)
	if err != nil {
		return domain.User{}, "", time.Time{}, err
	}
	if err := s.auth.TouchUserLogin(ctx, user.ID); err != nil {
		// Non-fatal: the session is already valid, and last_login_at is a
		// statistic, not a credential.
		logAuthWarning("TouchUserLogin", err)
	}
	return user, token, expiresAt, nil
}

func (s *Service) openSession(ctx context.Context, userID int64) (string, time.Time, error) {
	token, err := newSessionToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().UTC().Add(sessionTTL)
	if err := s.auth.CreateSession(ctx, token, userID, expiresAt); err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

// UserBySession resolves a cookie value to an account. An expired or unknown
// token is an error, never an anonymous success.
func (s *Service) UserBySession(ctx context.Context, token string) (domain.User, error) {
	if strings.TrimSpace(token) == "" {
		return domain.User{}, ErrInvalidCredentials
	}
	return s.auth.UserBySessionToken(ctx, token)
}

func (s *Service) Logout(ctx context.Context, token string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	return s.auth.DeleteSession(ctx, token)
}

func (s *Service) CountUsers(ctx context.Context) (int64, error) {
	return s.auth.CountUsers(ctx)
}

// DeleteNewsItem removes an article the operators judged off-topic. The
// automated filters catch the obvious cases (other sports, non-football
// namesakes) but a wrong article still slips through occasionally, and its
// sentiment feeds a player's Media score until someone removes it.
func (s *Service) DeleteNewsItem(ctx context.Context, id int64) (bool, error) {
	store, ok := s.auth.(interface {
		DeleteNewsItem(ctx context.Context, id int64) (bool, error)
	})
	if !ok {
		return false, errors.New("news deletion unavailable")
	}
	return store.DeleteNewsItem(ctx, id)
}
