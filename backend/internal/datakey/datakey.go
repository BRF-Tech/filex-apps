// Package datakey derives the keys the store protects its commercial data
// with from one secret, FAPPS_DATA_KEY (docs/ARCHITECTURE.md "Licensing"):
//
//   - "license-key": HMAC-SHA256 of a license key, the only form a key is
//     kept in (licenses.key_hash);
//   - "install-intents": AES-256-GCM for the license key an install intent
//     carries until it is used or expires;
//   - "instances": AES-256-GCM for a saved filex instance's address and
//     label;
//   - "instances-index": HMAC-SHA256 of an account and an address, so a
//     second save of the same address is found without reading any address;
//   - "notifications": AES-256-GCM for what a notification channel holds
//     that is a secret (a webhook's address, signing secret and header, a
//     browser's push endpoint and keys);
//   - "notifications-index": HMAC-SHA256 of a push endpoint, so the same
//     browser subscribing again finds its row.
//
// Each use has its own key (HKDF-SHA256, the use as the info), so one
// cannot open or forge another's data. Every sealed value is bound to its
// row and column with the AEAD's additional data.
package datakey

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

// HKDF infos, one per use.
const (
	infoLicenseKey     = "license-key"
	infoIntents        = "install-intents"
	infoInstances      = "instances"
	infoInstancesIndex = "instances-index"
	infoNotify         = "notifications"
	infoNotifyIndex    = "notifications-index"
)

// ErrSealed is returned for sealed data that does not open (another key,
// tampered, moved to another row).
var ErrSealed = errors.New("sealed data does not open")

// Keys are the derived keys.
type Keys struct {
	licenseMAC  []byte
	indexMAC    []byte
	notifyMAC   []byte
	intents     cipher.AEAD
	instances   cipher.AEAD
	notifySeals cipher.AEAD
}

// New derives the keys from FAPPS_DATA_KEY's bytes (at least 32).
func New(secret []byte) (*Keys, error) {
	if len(secret) < 32 {
		return nil, errors.New("FAPPS_DATA_KEY is shorter than 32 bytes")
	}
	derive := func(info string) ([]byte, error) { return hkdf.Key(sha256.New, secret, nil, info, 32) }
	k := &Keys{}
	var err error
	if k.licenseMAC, err = derive(infoLicenseKey); err != nil {
		return nil, err
	}
	if k.indexMAC, err = derive(infoInstancesIndex); err != nil {
		return nil, err
	}
	if k.intents, err = aead(derive, infoIntents); err != nil {
		return nil, err
	}
	if k.instances, err = aead(derive, infoInstances); err != nil {
		return nil, err
	}
	if k.notifyMAC, err = derive(infoNotifyIndex); err != nil {
		return nil, err
	}
	if k.notifySeals, err = aead(derive, infoNotify); err != nil {
		return nil, err
	}
	return k, nil
}

func aead(derive func(string) ([]byte, error), info string) (cipher.AEAD, error) {
	key, err := derive(info)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// LicenseKeyHash is what licenses.key_hash holds for a canonical key.
func (k *Keys) LicenseKeyHash(canonicalKey string) []byte {
	m := hmac.New(sha256.New, k.licenseMAC)
	m.Write([]byte(canonicalKey))
	return m.Sum(nil)
}

// InstanceIndex is saved_instances.url_index: the account and the origin,
// keyed, so the same address of two accounts gives two unrelated values.
func (k *Keys) InstanceIndex(accountID int64, origin string) []byte {
	m := hmac.New(sha256.New, k.indexMAC)
	var id [8]byte
	binary.BigEndian.PutUint64(id[:], uint64(accountID))
	m.Write(id[:])
	m.Write([]byte(origin))
	return m.Sum(nil)
}

// SealIntent seals an intent's license key; aad names the intent.
func (k *Keys) SealIntent(plain, aad []byte) ([]byte, error) { return seal(k.intents, plain, aad) }

// OpenIntent opens what SealIntent sealed with the same aad.
func (k *Keys) OpenIntent(sealed, aad []byte) ([]byte, error) { return open(k.intents, sealed, aad) }

// SealInstance seals a saved instance's field; aad names the row and field.
func (k *Keys) SealInstance(plain, aad []byte) ([]byte, error) { return seal(k.instances, plain, aad) }

// OpenInstance opens what SealInstance sealed with the same aad.
func (k *Keys) OpenInstance(sealed, aad []byte) ([]byte, error) {
	return open(k.instances, sealed, aad)
}

// SealNotify seals a notification channel's secret field; aad names the
// row and field.
func (k *Keys) SealNotify(plain, aad []byte) ([]byte, error) { return seal(k.notifySeals, plain, aad) }

// OpenNotify opens what SealNotify sealed with the same aad.
func (k *Keys) OpenNotify(sealed, aad []byte) ([]byte, error) {
	return open(k.notifySeals, sealed, aad)
}

// PushIndex is push_subscriptions.endpoint_index: a push endpoint, keyed.
func (k *Keys) PushIndex(endpoint string) []byte {
	m := hmac.New(sha256.New, k.notifyMAC)
	m.Write([]byte(endpoint))
	return m.Sum(nil)
}

func seal(a cipher.AEAD, plain, aad []byte) ([]byte, error) {
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return a.Seal(nonce, nonce, plain, aad), nil
}

func open(a cipher.AEAD, sealed, aad []byte) ([]byte, error) {
	n := a.NonceSize()
	if len(sealed) < n+a.Overhead() {
		return nil, ErrSealed
	}
	plain, err := a.Open(nil, sealed[:n], sealed[n:], aad)
	if err != nil {
		return nil, ErrSealed
	}
	return plain, nil
}
