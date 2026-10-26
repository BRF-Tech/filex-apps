// Package signedjson signs the store's answers that are not the index: the
// license verdict and the install intent (api/openapi.yaml, the
// conventions; domain.SignedEnvelope).
//
// ⚠⚠ The bytes are a contract with filex, which re-canonicalizes the
// payload it receives and verifies with plugin.VerifyDetached:
//
//   - canonical JSON: object keys sorted by their UTF-8 bytes, no
//     insignificant whitespace, no trailing newline, UTF-8, no HTML
//     escaping (encoding/json with SetEscapeHTML(false), the newline the
//     encoder appends trimmed). Numbers keep their exact text.
//   - signature: the key's ed25519 over the lower-hex sha256 of those bytes
//     (internal/signing, the scheme the index uses), lower-case hex.
//
// A payload's strings carry no control character and no U+2028 / U+2029
// (Go escapes those two, JavaScript does not): Canonical refuses them, so
// any JSON implementation that sorts keys gives the same bytes.
package signedjson

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/signing"
)

// ErrUnsafeText is returned for a payload string the canonical form would
// not write the same way in every JSON implementation.
var ErrUnsafeText = errors.New("payload text holds a control character or U+2028/U+2029")

// Canonical writes v's canonical bytes.
func Canonical(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return CanonicalBytes(raw)
}

// CanonicalBytes re-writes one JSON value canonically.
func CanonicalBytes(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("more than one JSON value")
	}
	if err := checkText(tree); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(tree); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func checkText(v any) error {
	switch x := v.(type) {
	case string:
		return safe(x)
	case map[string]any:
		for k, e := range x {
			if err := safe(k); err != nil {
				return err
			}
			if err := checkText(e); err != nil {
				return err
			}
		}
	case []any:
		for _, e := range x {
			if err := checkText(e); err != nil {
				return err
			}
		}
	}
	return nil
}

func safe(s string) error {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == ' ' || r == ' ' {
			return ErrUnsafeText
		}
	}
	return nil
}

// Sign builds the envelope for payload with key k.
func Sign(k *signing.Key, payload any) (domain.SignedEnvelope, error) {
	if k == nil {
		return domain.SignedEnvelope{}, errors.New("no signing key")
	}
	canon, err := Canonical(payload)
	if err != nil {
		return domain.SignedEnvelope{}, err
	}
	return domain.SignedEnvelope{Payload: canon, KeyID: k.ID, Signature: k.SignBytes(canon)}, nil
}

// Verify checks an envelope the way a filex does: re-canonicalize the
// payload as a generic tree, then the signature over its sha256 against the
// keys. It returns the canonical payload bytes.
func Verify(keys []ed25519.PublicKey, env domain.SignedEnvelope) ([]byte, error) {
	canon, err := CanonicalBytes(env.Payload)
	if err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	if err := signing.Verify(keys, signing.SHA256Hex(canon), env.Signature); err != nil {
		return nil, err
	}
	return canon, nil
}
