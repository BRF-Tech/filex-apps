package notify

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

// RFC 8291, section 5 and Appendix A (www.rfc-editor.org/rfc/rfc8291.txt):
// the worked example, byte for byte.
var rfc8291 = struct {
	plaintext, asPrivate, asPublic, uaPrivate, uaPublic, auth, salt, body string
}{
	plaintext: "When I grow up, I want to be a watermelon",
	asPrivate: "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw",
	asPublic:  "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8",
	uaPrivate: "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94",
	uaPublic:  "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
	auth:      "BTBZMqHH6r4Tts7J_aSIgg",
	salt:      "DGv6ra1nlYgDCS1FRnbzlw",
	body: "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PB" +
		"ru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN",
}

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := decodeB64(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEncryptPush_TheRFC8291Example(t *testing.T) {
	v := rfc8291
	as, err := ecdh.P256().NewPrivateKey(mustB64(t, v.asPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if got := b64.EncodeToString(as.PublicKey().Bytes()); got != v.asPublic {
		t.Fatalf("the application server's public key %s", got)
	}
	var target PushTarget
	target.Endpoint = "https://push.example.net/push/JzLQ3raZJfFBR0aqvOMsLrt54w4rJUsV"
	target.Keys.P256dh, target.Keys.Auth = v.uaPublic, v.auth
	body, err := EncryptPush([]byte(v.plaintext), target, mustB64(t, v.salt), as)
	if err != nil {
		t.Fatal(err)
	}
	if got := b64.EncodeToString(body); got != v.body {
		t.Fatalf("body\n got %s\nwant %s", got, v.body)
	}
	ua, err := ecdh.P256().NewPrivateKey(mustB64(t, v.uaPrivate))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := DecryptPush(mustB64(t, v.body), ua, mustB64(t, v.auth))
	if err != nil || string(plain) != v.plaintext {
		t.Fatalf("the RFC's body decrypts to %q, %v", plain, err)
	}
}

func TestEncryptPush_RoundTripWithFreshKeysAndARandomSalt(t *testing.T) {
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	var target PushTarget
	target.Keys.P256dh, target.Keys.Auth = b64.EncodeToString(ua.PublicKey().Bytes()), b64.EncodeToString(auth)
	payload := pushPayload(Message{Title: "Approved: filex-sign 1.2.0", Text: "listed", URL: "https://apps.example/apps/filex-sign"}, "t")
	a, err := EncryptPush(payload, target, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := EncryptPush(payload, target, nil, nil)
	if bytes.Equal(a, b) {
		t.Fatal("two encryptions of one message are equal (salt or key reused)")
	}
	plain, err := DecryptPush(a, ua, auth)
	if err != nil || !bytes.Equal(plain, payload) {
		t.Fatalf("round trip: %q, %v", plain, err)
	}
	var p PushPayload
	if err := json.Unmarshal(plain, &p); err != nil || p.Title != "Approved: filex-sign 1.2.0" || p.URL == "" {
		t.Fatalf("payload %+v, %v", p, err)
	}
	if _, err := EncryptPush(bytes.Repeat([]byte("x"), maxPushPlaintext+1), target, nil, nil); err == nil {
		t.Fatal("a message over one record was encrypted")
	}
	target.Keys.Auth = "c2hvcnQ"
	if _, err := EncryptPush(payload, target, nil, nil); err == nil {
		t.Fatal("a short auth secret was taken")
	}
}

func TestVAPID_ParsesAMatchingPairAndSignsAJWTForTheEndpointsOrigin(t *testing.T) {
	pub, priv, err := GenerateVAPID()
	if err != nil {
		t.Fatal(err)
	}
	v, err := ParseVAPID(pub, priv, "https://apps.example")
	if err != nil {
		t.Fatal(err)
	}
	if v.PublicKey() != pub {
		t.Fatal("public key changed")
	}
	now := time.Unix(1_800_000_000, 0)
	h, err := v.Authorization("https://fcm.googleapis.com/fcm/send/abc:def", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "vapid t=") || !strings.HasSuffix(h, ", k="+pub) {
		t.Fatalf("header %q", h)
	}
	jwt := strings.TrimSuffix(strings.TrimPrefix(h, "vapid t="), ", k="+pub)
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt %q", jwt)
	}
	var claims map[string]any
	if err := json.Unmarshal(mustB64(t, parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	if claims["aud"] != "https://fcm.googleapis.com" || claims["sub"] != "https://apps.example" ||
		int64(claims["exp"].(float64)) != now.Add(12*time.Hour).Unix() {
		t.Fatalf("claims %v", claims)
	}
	// The signature verifies with the public key (raw r||s, ES256).
	x, y := elliptic.Unmarshal(elliptic.P256(), mustB64(t, pub))
	key := &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}
	sig := mustB64(t, parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(key, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("the JWT's signature does not verify with the public key")
	}

	otherPub, _, _ := GenerateVAPID()
	for name, c := range map[string][3]string{
		"another public key": {otherPub, priv, "mailto:ops@example.com"},
		"short private key":  {pub, "AAAA", "mailto:ops@example.com"},
		"no subject scheme":  {pub, priv, "ops@example.com"},
		"http subject":       {pub, priv, "http://apps.example"},
	} {
		if _, err := ParseVAPID(c[0], c[1], c[2]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
