// Package signing is the store's half of filex's detached-signature scheme.
//
// ⚠⚠ ONE RULE, TWO PROGRAMS. filex verifies with
// backend/internal/plugin/signature.go (VerifyDetached): an ed25519
// signature over the LOWER-HEX sha256 of the signed bytes - the 64 ASCII
// characters, not the 32 raw bytes - given as hex or standard base64. The
// store signs exactly that, so a filex that trusts the store's key (its
// FILEX_PLUGIN_TRUSTED_KEYS for artifacts, its store key for the index)
// verifies with the code it already has. Changing what is signed here breaks
// every filex in the field; the cross-check in docs/ARCHITECTURE.md
// ("Testing") runs these vectors through filex's own VerifyDetached.
//
// The store writes signatures as lower-case hex.
package signing

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ErrSignatureInvalid is what Verify returns for a signature that does not
// verify against any of the keys.
var ErrSignatureInvalid = errors.New("signature invalid")

// SHA256Hex is the lower-case hex sha256 of b: the text that is signed.
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Key is a named ed25519 private key.
type Key struct {
	ID   string
	priv ed25519.PrivateKey
}

// ParsePrivateKey accepts a 32-byte seed or a 64-byte private key, as hex or
// standard base64.
func ParsePrivateKey(id, s string) (*Key, error) {
	raw, err := decode(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("signing key %q: %w", id, err)
	}
	switch len(raw) {
	case ed25519.SeedSize:
		return &Key{ID: id, priv: ed25519.NewKeyFromSeed(raw)}, nil
	case ed25519.PrivateKeySize:
		return &Key{ID: id, priv: ed25519.PrivateKey(raw)}, nil
	default:
		return nil, fmt.Errorf("signing key %q: %d bytes, want a %d-byte seed or a %d-byte private key",
			id, len(raw), ed25519.SeedSize, ed25519.PrivateKeySize)
	}
}

// NewKeyFromSeed builds a key from a 32-byte seed (tests, demo keys).
func NewKeyFromSeed(id string, seed []byte) (*Key, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("seed is %d bytes, want %d", len(seed), ed25519.SeedSize)
	}
	return &Key{ID: id, priv: ed25519.NewKeyFromSeed(seed)}, nil
}

// PublicHex is the public half as lower-case hex (what the index's `keys`
// and /v1/keys.json publish, and what a filex is configured with).
func (k *Key) PublicHex() string {
	return hex.EncodeToString(k.priv.Public().(ed25519.PublicKey))
}

// SignSHA256Hex signs a sha256 given as hex. The hex is lower-cased first:
// filex lower-cases before it verifies, so the two always sign and check the
// same 64 characters.
func (k *Key) SignSHA256Hex(sha string) string {
	return hex.EncodeToString(ed25519.Sign(k.priv, []byte(strings.ToLower(sha))))
}

// SignBytes signs the sha256 of b: what /v1/index.json.sig carries.
func (k *Key) SignBytes(b []byte) string {
	return k.SignSHA256Hex(SHA256Hex(b))
}

// StorageBuildPayloadV1 is the first line of the text a storage plugin
// build's signature is over.
const StorageBuildPayloadV1 = "filex-storage-build:v1"

// StorageBuildPayload is the exact text a storage plugin build's signature
// is over - filex's plugin.BuildClaim.Payload, byte for byte:
//
//	filex-storage-build:v1 LF <name> LF <version> LF <platform> LF <sha256>
//
// the name trimmed, the version trimmed and without a leading "v", the
// platform ("linux/amd64") and the sha256 lower case, joined by single line
// feeds, no line feed at the end. Signed as it is, not hashed again.
//
// ⚠⚠ ONE RULE, TWO PROGRAMS, like the sha256 form above: filex rebuilds this
// text from the install link (its name, version and this server's platform)
// and verifies it (backend/internal/plugin/signature.go VerifyBuild). A
// signature over the sha256 alone vouched for the bytes and for nothing else
// - an old, withdrawn build or another plugin's build verified just as well.
// The shared vector is in signing_test.go (TestStorageBuild_TheSharedVector)
// and in filex's signature_test.go.
func StorageBuildPayload(name, version, platform, sha string) []byte {
	return []byte(strings.Join([]string{
		StorageBuildPayloadV1,
		strings.TrimSpace(name),
		strings.TrimPrefix(strings.TrimSpace(version), "v"),
		strings.ToLower(strings.TrimSpace(platform)),
		strings.ToLower(strings.TrimSpace(sha)),
	}, "\n"))
}

// SignStorageBuild signs a storage plugin build: its name, version, platform
// and sha256 (StorageBuildPayload).
func (k *Key) SignStorageBuild(name, version, platform, sha string) string {
	return hex.EncodeToString(ed25519.Sign(k.priv, StorageBuildPayload(name, version, platform, sha)))
}

// VerifyStorageBuild is filex's VerifyBuild for the build form only: the
// signature (hex or base64) over StorageBuildPayload, against any of the keys.
func VerifyStorageBuild(keys []ed25519.PublicKey, name, version, platform, sha, signature string) error {
	sig, err := decode(strings.TrimSpace(signature))
	if err != nil {
		return fmt.Errorf("%w: neither hex nor base64", ErrSignatureInvalid)
	}
	payload := StorageBuildPayload(name, version, platform, sha)
	for _, pub := range keys {
		if ed25519.Verify(pub, payload, sig) {
			return nil
		}
	}
	return fmt.Errorf("%w: does not verify against any key", ErrSignatureInvalid)
}

// ParsePublicKey accepts an ed25519 public key as hex or standard base64.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	raw, err := decode(strings.TrimSpace(s))
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// Verify is filex's VerifyDetached for a non-empty key set: the signature
// (hex or base64) over the lower-hex sha256, against any of the keys.
func Verify(keys []ed25519.PublicKey, sha, signature string) error {
	sig, err := decode(strings.TrimSpace(signature))
	if err != nil {
		return fmt.Errorf("%w: neither hex nor base64", ErrSignatureInvalid)
	}
	for _, pub := range keys {
		if ed25519.Verify(pub, []byte(strings.ToLower(sha)), sig) {
			return nil
		}
	}
	return fmt.Errorf("%w: does not verify against any key", ErrSignatureInvalid)
}

// decode reads hex first, then standard base64 - the order filex reads them.
func decode(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("empty")
	}
	if b, err := hex.DecodeString(s); err == nil {
		return b, nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, errors.New("neither hex nor base64")
	}
	return b, nil
}
