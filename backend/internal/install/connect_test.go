package install

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/signing"
)

// requestVector is testdata/request-vector.json, made with Node's own crypto
// (make-request-vector.mjs): what a connected filex signs, computed by an
// implementation that is not this one. filex's appstore tests read the same
// vector, so the two sides cannot drift apart unnoticed.
type requestVector struct {
	SeedHex        string `json:"seed_hex"`
	PublicKey      string `json:"public_key"`
	KeyFingerprint string `json:"key_fingerprint"`
	Method         string `json:"method"`
	Path           string `json:"path"`
	InstanceID     string `json:"instance_id"`
	Timestamp      string `json:"timestamp"`
	Nonce          string `json:"nonce"`
	Body           string `json:"body"`
	BodySHA256     string `json:"body_sha256"`
	TextSHA256     string `json:"text_sha256"`
	Signature      string `json:"signature"`
}

func loadVector(t *testing.T) (requestVector, ed25519.PrivateKey) {
	t.Helper()
	raw, err := os.ReadFile("testdata/request-vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var v requestVector
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	seed, _ := hex.DecodeString(v.SeedHex)
	return v, ed25519.NewKeyFromSeed(seed)
}

func TestRequestSignatureMatchesTheIndependentVector(t *testing.T) {
	v, priv := loadVector(t)
	pub := priv.Public().(ed25519.PublicKey)
	if hex.EncodeToString(pub) != v.PublicKey || KeyFingerprint(pub) != v.KeyFingerprint {
		t.Fatalf("key %x fingerprint %s, want %s %s", pub, KeyFingerprint(pub), v.PublicKey, v.KeyFingerprint)
	}
	text := RequestText(v.Method, v.Path, v.InstanceID, v.KeyFingerprint, v.Timestamp, v.Nonce, []byte(v.Body))
	lines := strings.Split(text, "\n")
	if len(lines) != 8 || lines[0] != "FAPPS-INSTANCE-REQUEST-1" || lines[7] != v.BodySHA256 {
		t.Fatalf("request text:\n%s", text)
	}
	if got := signing.SHA256Hex([]byte(text)); got != v.TextSHA256 {
		t.Fatalf("text sha256 %s, want %s", got, v.TextSHA256)
	}
	if got := SignRequest(priv, v.Method, v.Path, v.InstanceID, v.Timestamp, v.Nonce, []byte(v.Body)); got != v.Signature {
		t.Fatalf("signature %s, want %s", got, v.Signature)
	}
	if err := signing.Verify([]ed25519.PublicKey{pub}, v.TextSHA256, v.Signature); err != nil {
		t.Fatal(err)
	}
}

// Every part of the request is in what is signed: changing any one of them
// gives a text the vector's signature does not verify.
func TestEveryPartOfARequestIsSigned(t *testing.T) {
	v, priv := loadVector(t)
	pub := priv.Public().(ed25519.PublicKey)
	base := []string{v.Method, v.Path, v.InstanceID, v.KeyFingerprint, v.Timestamp, v.Nonce, v.Body}
	for i := range base {
		parts := append([]string(nil), base...)
		parts[i] += "x"
		text := RequestText(parts[0], parts[1], parts[2], parts[3], parts[4], parts[5], []byte(parts[6]))
		if err := signing.Verify([]ed25519.PublicKey{pub}, signing.SHA256Hex([]byte(text)), v.Signature); err == nil {
			t.Fatalf("part %d changed and the signature still verifies", i)
		}
	}
	// The method is compared in upper case, so "post" signs as POST.
	if RequestText("post", v.Path, v.InstanceID, v.KeyFingerprint, v.Timestamp, v.Nonce, []byte(v.Body)) !=
		RequestText("POST", v.Path, v.InstanceID, v.KeyFingerprint, v.Timestamp, v.Nonce, []byte(v.Body)) {
		t.Fatal("the method's case changes the text")
	}
}

func TestConnectionCodesAndHeadersHaveOneShape(t *testing.T) {
	for code, ok := range map[string]bool{
		"fxc_" + strings.Repeat("A", 43): true,
		"fxc_" + strings.Repeat("A", 42): false,
		"FXC_" + strings.Repeat("A", 43): false,
		"fxc_" + strings.Repeat("+", 43): false,
		strings.Repeat("A", 43):          false,
	} {
		if connectCodeRe.MatchString(code) != ok {
			t.Errorf("code %q: match %v", code, !ok)
		}
	}
	for n, ok := range map[string]bool{
		"n0nce_AAAAAAAAAAAAAAAA": true, "short": false, strings.Repeat("a", 65): false, "has space aaaaaaaaaa": false,
	} {
		if nonceRe.MatchString(n) != ok {
			t.Errorf("nonce %q: match %v", n, !ok)
		}
	}
}
