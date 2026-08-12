package directory

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Crimson3076/RomM-Swarm/protocol"
)

// SessionToken is a bearer credential for an owner session, in the form
// handed back to the caller. Like auth.Token, the Host keeps only its hash.
type SessionToken string

// sessionTokenBytes is the entropy in a session token.
const sessionTokenBytes = 32

// sessionLifetime bounds how long an owner session stays valid.
const sessionLifetime = 24 * time.Hour

func newSessionToken() (SessionToken, error) {
	b := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("directory: generating a session token: %w", err)
	}
	return SessionToken(base64.RawURLEncoding.EncodeToString(b)), nil
}

func (t SessionToken) hash() string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

var (
	// ErrOwnerAlreadyExists means BootstrapOwner was called after an owner
	// account was already created.
	ErrOwnerAlreadyExists = errors.New("directory: an owner account already exists")

	// ErrInvalidCredentials covers both an unknown username and a wrong
	// password, deliberately not distinguished — a login endpoint that
	// says which one was wrong hands an attacker a username oracle.
	ErrInvalidCredentials = errors.New("directory: invalid username or password")

	// ErrSessionInvalid covers an unknown, expired, or malformed session
	// token.
	ErrSessionInvalid = errors.New("directory: session is invalid or has expired")
)

// OwnerExists reports whether the Host's single owner account has been
// created yet — what host/hostui's /setup page gates on, the same way
// bridge/adminui's /setup checks AdminPasswordHash.
func (d *Directory) OwnerExists(ctx context.Context) (bool, error) {
	var exists bool
	if err := d.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE is_owner)`).Scan(&exists); err != nil {
		return false, fmt.Errorf("directory: checking for an existing owner: %w", err)
	}
	return exists, nil
}

// BootstrapOwner creates the Host's single owner account. Fails once an
// owner already exists — the JSON-API analogue of bridge/adminui's /setup
// gate.
func (d *Directory) BootstrapOwner(ctx context.Context, username, displayName, email, password string) (protocol.UserID, error) {
	exists, err := d.OwnerExists(ctx)
	if err != nil {
		return "", err
	}
	if exists {
		return "", ErrOwnerAlreadyExists
	}

	hash, err := hashPassword(password)
	if err != nil {
		return "", err
	}

	id := protocol.NewUserID()
	var emailArg any
	if email != "" {
		emailArg = email
	}
	_, err = d.DB.ExecContext(ctx, `
		INSERT INTO accounts (id, username, display_name, email, password_hash, is_owner, created_at)
		VALUES ($1, $2, $3, $4, $5, TRUE, $6)`,
		string(id), username, displayName, emailArg, hash, d.now())
	if err != nil {
		return "", fmt.Errorf("directory: creating the owner account: %w", err)
	}

	_ = d.events.Record(ctx, protocol.Event{Kind: protocol.EventUserRegistered, At: d.now(), ActorUser: id})
	return id, nil
}

// Authenticate verifies username/password and issues a new session.
func (d *Directory) Authenticate(ctx context.Context, username, password string) (SessionToken, time.Time, error) {
	var (
		id           protocol.UserID
		passwordHash string
		disabledAt   sql.NullTime
	)
	err := d.DB.QueryRowContext(ctx, `
		SELECT id, password_hash, disabled_at FROM accounts WHERE username = $1`, username,
	).Scan(&id, &passwordHash, &disabledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, ErrInvalidCredentials
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf("directory: looking up account: %w", err)
	}
	if disabledAt.Valid || !verifyPassword(password, passwordHash) {
		return "", time.Time{}, ErrInvalidCredentials
	}

	token, err := newSessionToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := d.now().Add(sessionLifetime)
	_, err = d.DB.ExecContext(ctx, `
		INSERT INTO sessions (token_hash, account_id, created_at, expires_at)
		VALUES ($1, $2, $3, $4)`,
		token.hash(), string(id), d.now(), expiresAt)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("directory: creating session: %w", err)
	}
	return token, expiresAt, nil
}

// ValidateSession resolves a session token to the account it belongs to.
func (d *Directory) ValidateSession(ctx context.Context, token SessionToken) (protocol.UserID, error) {
	var (
		id        protocol.UserID
		expiresAt time.Time
	)
	err := d.DB.QueryRowContext(ctx, `
		SELECT account_id, expires_at FROM sessions WHERE token_hash = $1`, token.hash(),
	).Scan(&id, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrSessionInvalid
	}
	if err != nil {
		return "", fmt.Errorf("directory: looking up session: %w", err)
	}
	if d.now().After(expiresAt) {
		return "", ErrSessionInvalid
	}
	return id, nil
}

// Logout revokes a session token. Not an error if it's already gone.
func (d *Directory) Logout(ctx context.Context, token SessionToken) error {
	_, err := d.DB.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = $1`, token.hash())
	if err != nil {
		return fmt.Errorf("directory: revoking session: %w", err)
	}
	return nil
}
