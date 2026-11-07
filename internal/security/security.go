package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(hash), err
}

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func RandomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		panic(err)
	}
	return b
}

func Token(n int) string {
	return base64.RawURLEncoding.EncodeToString(RandomBytes(n))
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// Sealer encrypts small JSON secrets at rest (agent env, MCP headers).
type Sealer struct {
	aead cipher.AEAD
}

func LoadOrCreateSealer(path string) (*Sealer, error) {
	var key []byte
	b, err := os.ReadFile(path)
	if err == nil {
		key, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("invalid secret key at %s", path)
		}
	} else if errors.Is(err, os.ErrNotExist) {
		key = RandomBytes(32)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		enc := base64.RawStdEncoding.EncodeToString(key)
		if err := os.WriteFile(path, []byte(enc), 0o600); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

func (s *Sealer) Seal(plain []byte) (string, error) {
	if s == nil {
		return "", errors.New("sealer is not configured")
	}
	nonce := RandomBytes(s.aead.NonceSize())
	out := s.aead.Seal(nonce, nonce, plain, nil)
	return base64.RawStdEncoding.EncodeToString(out), nil
}

func (s *Sealer) Open(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, nil
	}
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	n := s.aead.NonceSize()
	if len(raw) < n {
		return nil, errors.New("ciphertext too short")
	}
	return s.aead.Open(nil, raw[:n], raw[n:], nil)
}

// SafePath resolves path inside root and rejects traversal/symlink escapes.
func SafePath(root, path string, mustExist bool) (string, error) {
	if root == "" {
		return "", errors.New("empty root")
	}
	if !filepath.IsAbs(root) || !filepath.IsAbs(path) {
		return "", errors.New("paths must be absolute")
	}
	rootAbs := filepath.Clean(root)
	pathAbs := filepath.Clean(path)
	rel, err := filepath.Rel(rootAbs, pathAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q is outside the workspace", path)
	}
	check := pathAbs
	// Resolve the closest existing ancestor so symlinks cannot escape the root.
	for {
		if _, err := os.Lstat(check); err == nil {
			break
		}
		parent := filepath.Dir(check)
		if parent == check {
			break
		}
		check = parent
	}
	if resolved, err := filepath.EvalSymlinks(check); err == nil {
		// Resolve the root the same way: EvalSymlinks normalizes Windows
		// 8.3 short names (e.g. RUNNER~1 -> runneradmin), which would
		// otherwise make a legitimately inside path look like an escape.
		rootCmp := rootAbs
		if r, err := filepath.EvalSymlinks(rootAbs); err == nil {
			rootCmp = r
		}
		if rel, err := filepath.Rel(rootCmp, resolved); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("path %q escapes the workspace via symlink", path)
		}
	}
	if mustExist {
		if _, err := os.Stat(pathAbs); err != nil {
			return "", err
		}
	}
	return pathAbs, nil
}
