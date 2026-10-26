package signing

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

type vector struct {
	Name         string `json:"name"`
	SeedHex      string `json:"seed_hex"`
	PublicHex    string `json:"public_hex"`
	MessageHex   string `json:"message_hex"`
	SHA256Hex    string `json:"sha256_hex"`
	SignatureHex string `json:"signature_hex"`
}

func loadVectors(t *testing.T) []vector {
	t.Helper()
	b, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Vectors []vector `json:"vectors"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Vectors) == 0 {
		t.Fatal("no vectors")
	}
	return doc.Vectors
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The vectors were written by Node's crypto, not by this package: the Go
// signer must produce the same bytes, which pins the scheme (what is signed
// is the 64-character hex, not the raw digest).
func TestSign_MatchesTheIndependentVectors(t *testing.T) {
	for _, v := range loadVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			k, err := NewKeyFromSeed("k", mustHex(t, v.SeedHex))
			if err != nil {
				t.Fatal(err)
			}
			if got := k.PublicHex(); got != v.PublicHex {
				t.Fatalf("public key = %s, want %s", got, v.PublicHex)
			}
			msg := mustHex(t, v.MessageHex)
			if got := SHA256Hex(msg); got != v.SHA256Hex {
				t.Fatalf("sha256 = %s, want %s", got, v.SHA256Hex)
			}
			if got := k.SignBytes(msg); got != v.SignatureHex {
				t.Fatalf("signature = %s, want %s", got, v.SignatureHex)
			}
		})
	}
}

func TestVerify_AcceptsHexBase64AndUpperCaseDigest(t *testing.T) {
	for _, v := range loadVectors(t) {
		pub, err := ParsePublicKey(v.PublicHex)
		if err != nil {
			t.Fatal(err)
		}
		keys := []ed25519.PublicKey{pub}
		if err := Verify(keys, v.SHA256Hex, v.SignatureHex); err != nil {
			t.Fatalf("%s hex: %v", v.Name, err)
		}
		b64 := base64.StdEncoding.EncodeToString(mustHex(t, v.SignatureHex))
		if err := Verify(keys, v.SHA256Hex, b64); err != nil {
			t.Fatalf("%s base64: %v", v.Name, err)
		}
		if err := Verify(keys, strings.ToUpper(v.SHA256Hex), v.SignatureHex); err != nil {
			t.Fatalf("%s upper-case digest: %v", v.Name, err)
		}
	}
}

func TestVerify_RefusesAChangedDigestAndAForeignKey(t *testing.T) {
	vs := loadVectors(t)
	if len(vs) < 2 {
		t.Skip("needs two vectors")
	}
	pub0, _ := ParsePublicKey(vs[0].PublicHex)
	pub1, _ := ParsePublicKey(vs[1].PublicHex)

	flipped := []byte(vs[0].SHA256Hex)
	if flipped[0] == '0' {
		flipped[0] = '1'
	} else {
		flipped[0] = '0'
	}
	if err := Verify([]ed25519.PublicKey{pub0}, string(flipped), vs[0].SignatureHex); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("changed digest: err = %v, want ErrSignatureInvalid", err)
	}
	if err := Verify([]ed25519.PublicKey{pub1}, vs[0].SHA256Hex, vs[0].SignatureHex); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("foreign key: err = %v, want ErrSignatureInvalid", err)
	}
	if err := Verify([]ed25519.PublicKey{pub0}, vs[0].SHA256Hex, "not a signature"); !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("garbage: err = %v, want ErrSignatureInvalid", err)
	}
}

func TestParsePrivateKey_SeedAndFullKeyAgree(t *testing.T) {
	v := loadVectors(t)[0]
	seed := mustHex(t, v.SeedHex)
	a, err := ParsePrivateKey("a", v.SeedHex)
	if err != nil {
		t.Fatal(err)
	}
	full := ed25519.NewKeyFromSeed(seed)
	b, err := ParsePrivateKey("b", base64.StdEncoding.EncodeToString(full))
	if err != nil {
		t.Fatal(err)
	}
	if a.PublicHex() != b.PublicHex() || a.PublicHex() != v.PublicHex {
		t.Fatalf("seed and full key disagree: %s / %s", a.PublicHex(), b.PublicHex())
	}
	if _, err := ParsePrivateKey("c", "abcd"); err == nil {
		t.Fatal("a 2-byte key was accepted")
	}
}

// The storage plugin build text, pinned by one vector that filex's
// backend/internal/plugin/signature_test.go (TestVerifyBuild_TheSharedVector)
// holds too: seed 32 x 0x07, myfs 1.3.0 on linux/amd64, sha256 64 x "1". The
// signature was made by Go's crypto/ed25519 over the text, not by this
// package: if either program changes what is signed, both tests go red.
func TestStorageBuild_TheSharedVector(t *testing.T) {
	const (
		publicHex  = "ea4a6c63e29c520abef5507b132ec5f9954776aebebe7b92421eea691446d22c"
		payloadHex = "66696c65782d73746f726167652d6275696c643a76310a6d7966730a312e332e300a6c696e75782f616d6436340a31313131313131313131313131313131313131313131313131313131313131313131313131313131313131313131313131313131313131313131313131313131"
		sigHex     = "bc0be1ad6a3b7411c69dbe14e53c78192070a068843844173f01c2df8ada9a1d8da12d30c3f8ba5c07b34511c8600005d2b4cb7ccf4134c18da558c803bfad09"
	)
	sha := strings.Repeat("1", 64)
	if got := hex.EncodeToString(StorageBuildPayload("myfs", "1.3.0", "linux/amd64", sha)); got != payloadHex {
		t.Fatalf("the build text changed:\n got %s\nwant %s", got, payloadHex)
	}
	k, err := NewKeyFromSeed("k", mustHex(t, strings.Repeat("07", 32)))
	if err != nil {
		t.Fatal(err)
	}
	if k.PublicHex() != publicHex {
		t.Fatalf("public %s", k.PublicHex())
	}
	if got := k.SignStorageBuild("myfs", "v1.3.0", "Linux/AMD64", strings.ToUpper(sha)); got != sigHex {
		t.Fatalf("signature %s, want %s", got, sigHex)
	}
	pub := ed25519.PublicKey(mustHex(t, publicHex))
	if err := VerifyStorageBuild([]ed25519.PublicKey{pub}, "myfs", "1.3.0", "linux/amd64", sha, sigHex); err != nil {
		t.Fatal(err)
	}
	if err := Verify([]ed25519.PublicKey{pub}, sha, sigHex); err == nil {
		t.Fatal("the build signature verifies as a signature over the sha256 alone")
	}
}
