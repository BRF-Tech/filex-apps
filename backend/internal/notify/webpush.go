package notify

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// Web Push, written from the standards with the standard library:
//
//   - RFC 8291 (message encryption): ECDH on P-256 with the browser's key,
//     HKDF-SHA256 with its auth secret, one aes128gcm record (RFC 8188);
//   - RFC 8292 (VAPID): the store proves which server it is to the push
//     service with an ES256 JWT signed by FAPPS_VAPID_PRIVATE_KEY, and the
//     browser subscribed with the matching public key.
//
// A push service is an address the browser handed over, so it is reached
// through the guarded client like a webhook (send.go).

// b64 is the URL-safe base64 alphabet without padding, as the Push API
// gives keys and as VAPID writes them.
var b64 = base64.RawURLEncoding

// decodeB64 reads URL-safe or standard base64, padded or not.
func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, nil
		}
	}
	return nil, errors.New("not base64")
}

// VAPID is the store's application server key pair.
type VAPID struct {
	priv *ecdsa.PrivateKey
	// Public is the uncompressed P-256 point (65 bytes): the browser's
	// applicationServerKey.
	Public []byte
	// Subject is the contact the push service may use (mailto: or https:).
	Subject string
}

// ParseVAPID reads the key pair (base64url: the 32-byte private scalar and
// the 65-byte public point, as web-push tools print them) and checks that
// they belong together.
func ParseVAPID(public, private, subject string) (*VAPID, error) {
	privBytes, err := decodeB64(private)
	if err != nil || len(privBytes) != 32 {
		return nil, errors.New("FAPPS_VAPID_PRIVATE_KEY is not a base64url P-256 private key (32 bytes)")
	}
	pubBytes, err := decodeB64(public)
	if err != nil || len(pubBytes) != 65 || pubBytes[0] != 4 {
		return nil, errors.New("FAPPS_VAPID_PUBLIC_KEY is not a base64url uncompressed P-256 public key (65 bytes)")
	}
	priv, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), privBytes)
	if err != nil {
		return nil, fmt.Errorf("FAPPS_VAPID_PRIVATE_KEY: %w", err)
	}
	derived, err := priv.PublicKey.Bytes()
	if err != nil {
		return nil, err
	}
	if !hmac.Equal(derived, pubBytes) {
		return nil, errors.New("FAPPS_VAPID_PUBLIC_KEY is not the public half of FAPPS_VAPID_PRIVATE_KEY")
	}
	if !strings.HasPrefix(subject, "mailto:") && !strings.HasPrefix(subject, "https://") {
		return nil, errors.New("FAPPS_VAPID_SUBJECT must be a mailto: or https: address")
	}
	return &VAPID{priv: priv, Public: pubBytes, Subject: subject}, nil
}

// GenerateVAPID makes a new key pair, base64url (the operator CLI's
// vapid-keygen).
func GenerateVAPID() (public, private string, err error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	pub, err := k.PublicKey.Bytes()
	if err != nil {
		return "", "", err
	}
	raw, err := k.Bytes()
	if err != nil {
		return "", "", err
	}
	return b64.EncodeToString(pub), b64.EncodeToString(raw), nil
}

// PublicKey is the public key as the browser takes it (base64url).
func (v *VAPID) PublicKey() string { return b64.EncodeToString(v.Public) }

// Authorization is the request's Authorization header for a push endpoint:
// "vapid t=<JWT>, k=<public key>", the JWT's audience the endpoint's origin,
// valid 12 hours.
func (v *VAPID) Authorization(endpoint string, now time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("bad push endpoint")
	}
	header := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{
		"aud": u.Scheme + "://" + u.Host,
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": v.Subject,
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + b64.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, v.priv, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + signing + "." + b64.EncodeToString(sig) + ", k=" + v.PublicKey(), nil
}

// PushTarget is a browser's subscription (PushSubscription.toJSON()).
type PushTarget struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

// keys reads the browser's public key and auth secret.
func (t PushTarget) keys() (*ecdh.PublicKey, []byte, error) {
	pub, err := decodeB64(t.Keys.P256dh)
	if err != nil {
		return nil, nil, errors.New("keys.p256dh is not base64url")
	}
	uaPublic, err := ecdh.P256().NewPublicKey(pub)
	if err != nil {
		return nil, nil, errors.New("keys.p256dh is not a P-256 public key")
	}
	auth, err := decodeB64(t.Keys.Auth)
	if err != nil || len(auth) != 16 {
		return nil, nil, errors.New("keys.auth is not a 16-byte base64url secret")
	}
	return uaPublic, auth, nil
}

// recordSize is the aes128gcm record size written in the header (one record
// carries the whole message).
const recordSize = 4096

// maxPushPlaintext keeps one record: a push service takes at least 4096
// bytes of body; the header (86) and the tag (16) and the delimiter (1)
// come out of it.
const maxPushPlaintext = 3993

// EncryptPush encrypts payload for the browser (RFC 8291). salt and
// asPrivate are random when nil (tests pass the RFC's).
func EncryptPush(payload []byte, t PushTarget, salt []byte, asPrivate *ecdh.PrivateKey) ([]byte, error) {
	if len(payload) > maxPushPlaintext {
		return nil, errors.New("push message too large")
	}
	uaPublic, auth, err := t.keys()
	if err != nil {
		return nil, err
	}
	if asPrivate == nil {
		if asPrivate, err = ecdh.P256().GenerateKey(rand.Reader); err != nil {
			return nil, err
		}
	}
	if salt == nil {
		salt = make([]byte, 16)
		if _, err := io.ReadFull(rand.Reader, salt); err != nil {
			return nil, err
		}
	}
	shared, err := asPrivate.ECDH(uaPublic)
	if err != nil {
		return nil, err
	}
	uaBytes, asBytes := uaPublic.Bytes(), asPrivate.PublicKey().Bytes()

	// IKM = HKDF(auth, ecdh_secret, "WebPush: info" || 0x00 || ua_public || as_public, 32)
	keyInfo := append(append([]byte("WebPush: info\x00"), uaBytes...), asBytes...)
	ikm, err := hkdf.Key(sha256.New, shared, auth, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	// CEK and NONCE from the salt (RFC 8188).
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// One record, the last one: the plaintext and the 0x02 delimiter.
	plain := append(append([]byte{}, payload...), 0x02)
	header := make([]byte, 0, 16+4+1+len(asBytes))
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, recordSize)
	header = append(header, byte(len(asBytes)))
	header = append(header, asBytes...)
	return gcm.Seal(header, nonce, plain, nil), nil
}

// DecryptPush is the browser's side of EncryptPush (tests: the round trip
// and the RFC's vector checked both ways).
func DecryptPush(body []byte, uaPrivate *ecdh.PrivateKey, auth []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("short body")
	}
	salt, idLen := body[:16], int(body[20])
	if len(body) < 21+idLen {
		return nil, errors.New("short header")
	}
	asPublic, err := ecdh.P256().NewPublicKey(body[21 : 21+idLen])
	if err != nil {
		return nil, err
	}
	shared, err := uaPrivate.ECDH(asPublic)
	if err != nil {
		return nil, err
	}
	keyInfo := append(append([]byte("WebPush: info\x00"), uaPrivate.PublicKey().Bytes()...), asPublic.Bytes()...)
	ikm, err := hkdf.Key(sha256.New, shared, auth, string(keyInfo), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, body[21+idLen:], nil)
	if err != nil {
		return nil, err
	}
	i := len(plain) - 1
	for i >= 0 && plain[i] == 0 {
		i--
	}
	if i < 0 || plain[i] != 0x02 {
		return nil, errors.New("no last-record delimiter")
	}
	return plain[:i], nil
}

// PushPayload is what the store's service worker reads from a push.
type PushPayload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	// Tag groups repeats of the same event in the browser.
	Tag string `json:"tag"`
}

// pushPayload is a message as the service worker reads it, clipped to fit
// one record.
func pushPayload(m Message, tag string) []byte {
	p := PushPayload{Title: clip(m.Title, 200), Body: clip(m.Text, 1000), URL: m.URL, Tag: tag}
	b, _ := marshal(p)
	return b
}
