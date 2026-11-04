package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/security"
	"github.com/jahrulnr/nusamation/internal/store"
)

const (
	PasswordHashKey = "auth.password_hash"
	InitializedKey  = "auth.initialized"
	BootstrapKey    = "auth.bootstrap_completed_at"
)

var ErrNotInitialized = errors.New("auth is not initialized")

// EnsureBootstrap creates the single-admin password on first run. It returns
// the generated password (only when it created one) so the CLI can print it.
func EnsureBootstrap(ctx context.Context, st *store.Store, envPassword string) (password string, created bool, err error) {
	if _, err := st.GetSetting(ctx, PasswordHashKey); err == nil {
		return "", false, nil
	}
	password = strings.TrimSpace(envPassword)
	if password == "" {
		password = "nm_" + security.Token(9)
	}
	if len(password) < 4 {
		return "", false, fmt.Errorf("NUSAMATION_PASSWORD must be at least 4 characters")
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return "", false, err
	}
	if err := st.SetSetting(ctx, PasswordHashKey, hash); err != nil {
		return "", false, err
	}
	_ = st.SetSetting(ctx, InitializedKey, "1")
	_ = st.SetSetting(ctx, BootstrapKey, fmt.Sprintf("%d", time.Now().UnixMilli()))
	return password, true, nil
}

func Initialized(ctx context.Context, st *store.Store) bool {
	_, err := st.GetSetting(ctx, PasswordHashKey)
	return err == nil
}

func Verify(ctx context.Context, st *store.Store, password string) bool {
	hash, err := st.GetSetting(ctx, PasswordHashKey)
	if err != nil || hash == "" {
		return false
	}
	return security.CheckPassword(hash, password)
}

func SetPassword(ctx context.Context, st *store.Store, password string) error {
	if len(password) < 4 {
		return fmt.Errorf("password must be at least 4 characters")
	}
	hash, err := security.HashPassword(password)
	if err != nil {
		return err
	}
	if err := st.SetSetting(ctx, PasswordHashKey, hash); err != nil {
		return err
	}
	_ = st.SetSetting(ctx, InitializedKey, "1")
	return nil
}

func CreateSession(ctx context.Context, st *store.Store, ip, ua string, ttl time.Duration) (string, store.Session, error) {
	token := security.Token(32)
	csrf := security.Token(24)
	hash := security.HashToken(token)
	if err := st.CreateSession(ctx, hash, csrf, ip, ua, ttl.Milliseconds()); err != nil {
		return "", store.Session{}, err
	}
	ss, err := st.GetSession(ctx, hash)
	if err != nil {
		return "", store.Session{}, err
	}
	return token, ss, nil
}

func LookupSession(ctx context.Context, st *store.Store, token string) (store.Session, error) {
	if token == "" {
		return store.Session{}, ErrNotInitialized
	}
	ss, err := st.GetSession(ctx, security.HashToken(token))
	if err != nil {
		return ss, err
	}
	if ss.ExpiresAt < time.Now().UnixMilli() {
		_ = st.DeleteSession(ctx, ss.IDHash)
		return store.Session{}, store.ErrNotFound
	}
	return ss, nil
}

func DeleteSession(ctx context.Context, st *store.Store, token string) {
	if token == "" {
		return
	}
	_ = st.DeleteSession(ctx, security.HashToken(token))
}
