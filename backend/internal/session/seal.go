package session

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Purposes a key is derived for: one secret, independent keys.
const (
	purposeGitHubToken = "fapps/v1/github-token"
	purposeFlow        = "fapps/v1/oauth-flow"
)

// ErrSealed is returned for sealed data that does not open (wrong key,
// tampered, moved to another context).
var ErrSealed = errors.New("sealed data does not open")

// sealer is AES-256-GCM with a key derived from the session secret.
type sealer struct {
	aead cipher.AEAD
}

func newSealer(secret []byte, purpose string) (*sealer, error) {
	key, err := hkdf.Key(sha256.New, secret, nil, purpose, 32)
	if err != nil {
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
	return &sealer{aead: aead}, nil
}

// seal encrypts and authenticates plain; aad binds it to a context (the
// session it belongs to).
func (s *sealer) seal(plain, aad []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, plain, aad), nil
}

func (s *sealer) open(sealed, aad []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < n+s.aead.Overhead() {
		return nil, ErrSealed
	}
	plain, err := s.aead.Open(nil, sealed[:n], sealed[n:], aad)
	if err != nil {
		return nil, ErrSealed
	}
	return plain, nil
}

// ParseSecret reads FAPPS_SESSION_KEY: at least 32 bytes, base64 (standard
// or URL alphabet, padded or not).
func ParseSecret(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			if len(b) < 32 {
				return nil, fmt.Errorf("FAPPS_SESSION_KEY is %d bytes, at least 32 are needed", len(b))
			}
			return b, nil
		}
	}
	return nil, errors.New("FAPPS_SESSION_KEY is not base64")
}
