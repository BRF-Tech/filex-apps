package signedjson

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/signing"
)

type vectors struct {
	SeedHex   string `json:"seed_hex"`
	PublicHex string `json:"public_hex"`
	Cases     []struct {
		Payload   json.RawMessage `json:"payload"`
		Canonical string          `json:"canonical"`
		SHA256    string          `json:"sha256"`
		Signature string          `json:"signature"`
	} `json:"cases"`
}

func loadVectors(t *testing.T) (vectors, *signing.Key) {
	t.Helper()
	raw, err := os.ReadFile("testdata/envelope-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	seed, _ := hex.DecodeString(v.SeedHex)
	k, err := signing.NewKeyFromSeed("vec", seed)
	if err != nil {
		t.Fatal(err)
	}
	if k.PublicHex() != v.PublicHex {
		t.Fatalf("public key %s, Node says %s", k.PublicHex(), v.PublicHex)
	}
	return v, k
}

// The vectors were made by Node (testdata/make-vectors.mjs: JSON.stringify
// of the key-sorted tree, node:crypto ed25519): the Go side must write the
// same bytes and, ed25519 being deterministic, the same signatures.
func TestCanonicalBytesAndSignaturesMatchNode(t *testing.T) {
	v, k := loadVectors(t)
	if len(v.Cases) < 4 {
		t.Fatalf("%d cases", len(v.Cases))
	}
	for i, c := range v.Cases {
		got, err := CanonicalBytes(c.Payload)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if string(got) != c.Canonical {
			t.Fatalf("case %d canonical:\n go   %s\n node %s", i, got, c.Canonical)
		}
		sum := sha256.Sum256(got)
		if hex.EncodeToString(sum[:]) != c.SHA256 {
			t.Fatalf("case %d sha256", i)
		}
		env, err := Sign(k, c.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if env.Signature != c.Signature || env.KeyID != "vec" || string(env.Payload) != c.Canonical {
			t.Fatalf("case %d: signature %s, Node %s", i, env.Signature, c.Signature)
		}
	}
}

// Verified with crypto/ed25519 directly (not internal/signing): the signed
// message is the 64 lower-hex characters of the canonical bytes' sha256.
func TestEnvelopeVerifiesWithPlainEd25519(t *testing.T) {
	_, k := loadVectors(t)
	pub, _ := hex.DecodeString(k.PublicHex())
	payload := domain.LicenseVerdict{Result: domain.LicenseValid, App: "hello", Licensee: "A <b> & c", Seats: 2,
		InstanceID: "i-1", CheckedAt: "2026-10-04T12:00:00Z", NextCheckBy: "2026-10-05T12:00:00Z", GraceUntil: "2026-10-18T12:00:00Z"}
	used := 1
	payload.SeatsUsed = &used
	env, err := Sign(k, payload)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"app":"hello","checked_at":"2026-10-04T12:00:00Z","grace_until":"2026-10-18T12:00:00Z","instance_id":"i-1",` +
		`"licensee":"A <b> & c","next_check_by":"2026-10-05T12:00:00Z","result":"valid","seats":2,"seats_used":1}`
	if string(env.Payload) != want {
		t.Fatalf("payload\n %s\nwant\n %s", env.Payload, want)
	}
	sum := sha256.Sum256([]byte(want))
	sig, _ := hex.DecodeString(env.Signature)
	if !ed25519.Verify(ed25519.PublicKey(pub), []byte(hex.EncodeToString(sum[:])), sig) {
		t.Fatal("plain ed25519 does not verify the envelope")
	}
	// The envelope as a filex receives it: a JSON object with the payload
	// inline, whose whitespace and key order a proxy might change.
	body, _ := json.Marshal(env)
	var back domain.SignedEnvelope
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatal(err)
	}
	back.Payload = json.RawMessage("{ \"seats_used\": 1, \"seats\": 2, \"result\": \"valid\", \"next_check_by\": \"2026-10-05T12:00:00Z\"," +
		" \"licensee\": \"A <b> & c\", \"instance_id\": \"i-1\", \"grace_until\": \"2026-10-18T12:00:00Z\"," +
		" \"checked_at\": \"2026-10-04T12:00:00Z\", \"app\": \"hello\" }")
	if _, err := Verify([]ed25519.PublicKey{pub}, back); err != nil {
		t.Fatalf("a reordered payload: %v", err)
	}
	back.Payload = json.RawMessage(`{"app":"hello","checked_at":"2026-10-04T12:00:00Z","grace_until":"2026-10-18T12:00:00Z","instance_id":"i-1","licensee":"A <b> & c","next_check_by":"2026-10-05T12:00:00Z","result":"valid","seats":3,"seats_used":1}`)
	if _, err := Verify([]ed25519.PublicKey{pub}, back); !errors.Is(err, signing.ErrSignatureInvalid) {
		t.Fatalf("a changed payload verified: %v", err)
	}
}

func TestOptionalFieldsAreAbsentNotNull(t *testing.T) {
	canon, err := Canonical(domain.LicenseVerdict{Result: domain.LicenseInvalid, App: "x", InstanceID: "i",
		CheckedAt: "c", NextCheckBy: "n", GraceUntil: "g"})
	if err != nil {
		t.Fatal(err)
	}
	if string(canon) != `{"app":"x","checked_at":"c","grace_until":"g","instance_id":"i","next_check_by":"n","result":"invalid"}` {
		t.Fatalf("%s", canon)
	}
	canon, _ = Canonical(domain.InstallIntentPayload{Store: "s", Permissions: []string{}})
	for _, k := range []string{"wasm_sha256", "ui_sha256", "license_key", "null"} {
		if json.Valid(canon) && containsKey(canon, k) {
			t.Fatalf("%s present in %s", k, canon)
		}
	}
}

func containsKey(b []byte, k string) bool {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	_, ok := m[k]
	return ok
}

func TestCanonicalRefusesTextJavaScriptWritesDifferently(t *testing.T) {
	for _, s := range []string{"line sep", "para sep", "tab\there", "nl\n", "bell\x07"} {
		if _, err := Canonical(map[string]string{"licensee": s}); !errors.Is(err, ErrUnsafeText) {
			t.Errorf("%q: %v", s, err)
		}
	}
	if _, err := Sign(nil, map[string]string{}); err == nil {
		t.Fatal("signed without a key")
	}
}
