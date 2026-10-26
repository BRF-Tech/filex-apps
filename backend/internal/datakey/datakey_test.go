package datakey

import (
	"bytes"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"testing"
)

func keys(t *testing.T, b byte) *Keys {
	t.Helper()
	k, err := New(bytes.Repeat([]byte{b}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestNewRefusesAShortSecret(t *testing.T) {
	if _, err := New(make([]byte, 31)); err == nil {
		t.Fatal("31 bytes accepted")
	}
}

// The license-key hash is HMAC-SHA256 under HKDF(secret, "license-key"):
// recomputed here without the package's own code path.
func TestLicenseKeyHashIsHMACUnderTheLicenseKeyDerivation(t *testing.T) {
	secret := bytes.Repeat([]byte{7}, 32)
	k, _ := New(secret)
	sub, err := hkdf.Key(sha256.New, secret, nil, "license-key", 32)
	if err != nil {
		t.Fatal(err)
	}
	m := hmac.New(sha256.New, sub)
	m.Write([]byte("FXL-ABCD-EFGH-JKMN-PQRS-TVWX-YZ01-23"))
	if !bytes.Equal(k.LicenseKeyHash("FXL-ABCD-EFGH-JKMN-PQRS-TVWX-YZ01-23"), m.Sum(nil)) {
		t.Fatal("the hash is not HMAC-SHA256 under HKDF(\"license-key\")")
	}
	if bytes.Equal(k.LicenseKeyHash("a"), keys(t, 8).LicenseKeyHash("a")) {
		t.Fatal("two secrets give the same hash")
	}
}

func TestInstanceIndexDependsOnTheAccount(t *testing.T) {
	k := keys(t, 1)
	a, b := k.InstanceIndex(1, "https://files.example.com"), k.InstanceIndex(2, "https://files.example.com")
	if bytes.Equal(a, b) {
		t.Fatal("the same address of two accounts gives the same index")
	}
	if !bytes.Equal(a, k.InstanceIndex(1, "https://files.example.com")) || len(a) != 32 {
		t.Fatal("the index is not stable")
	}
	if bytes.Contains(a, []byte("files.example.com")) {
		t.Fatal("the index carries the address")
	}
}

func TestSealedValuesAreBoundToTheirUseAndRow(t *testing.T) {
	k := keys(t, 1)
	plain := []byte("https://files.example.com")
	sealed, err := k.SealInstance(plain, []byte("row-1:url"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, plain) || bytes.Contains(sealed, []byte("example")) {
		t.Fatal("the sealed value carries the address")
	}
	if got, err := k.OpenInstance(sealed, []byte("row-1:url")); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("open = %q, %v", got, err)
	}
	for name, open := range map[string]func() ([]byte, error){
		"another row":    func() ([]byte, error) { return k.OpenInstance(sealed, []byte("row-2:url")) },
		"another field":  func() ([]byte, error) { return k.OpenInstance(sealed, []byte("row-1:label")) },
		"another use":    func() ([]byte, error) { return k.OpenIntent(sealed, []byte("row-1:url")) },
		"another secret": func() ([]byte, error) { return keys(t, 2).OpenInstance(sealed, []byte("row-1:url")) },
		"truncated":      func() ([]byte, error) { return k.OpenInstance(sealed[:10], []byte("row-1:url")) },
	} {
		if _, err := open(); !errors.Is(err, ErrSealed) {
			t.Errorf("%s: %v", name, err)
		}
	}
	again, _ := k.SealInstance(plain, []byte("row-1:url"))
	if bytes.Equal(again, sealed) {
		t.Fatal("two seals of the same value are equal (nonce reused)")
	}
}

func TestNotificationSealsAreTheirOwnUse(t *testing.T) {
	k := keys(t, 1)
	plain := []byte("https://hooks.example.com/services/T0/B0/secret")
	sealed, err := k.SealNotify(plain, []byte("hook-1:url"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := k.OpenNotify(sealed, []byte("hook-1:url")); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("open = %q, %v", got, err)
	}
	if _, err := k.OpenInstance(sealed, []byte("hook-1:url")); !errors.Is(err, ErrSealed) {
		t.Fatalf("an instance key opened a notification seal: %v", err)
	}
	if _, err := k.OpenNotify(sealed, []byte("hook-2:url")); !errors.Is(err, ErrSealed) {
		t.Fatalf("another row opened it: %v", err)
	}
	a, b := k.PushIndex("https://push.example.com/a"), k.PushIndex("https://push.example.com/b")
	if len(a) != 32 || bytes.Equal(a, b) || !bytes.Equal(a, k.PushIndex("https://push.example.com/a")) {
		t.Fatal("PushIndex is not a stable 32-byte keyed hash of the endpoint")
	}
	if bytes.Equal(a, keys(t, 2).PushIndex("https://push.example.com/a")) {
		t.Fatal("PushIndex does not depend on the secret")
	}
}
