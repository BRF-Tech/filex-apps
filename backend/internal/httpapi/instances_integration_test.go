//go:build integration

package httpapi_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/install"
)

// Instance connections (docs/ARCHITECTURE.md "Instance connections"): a
// person connects a saved instance with a one-time code, the
// filex server binds its key and then asks for install intents in its own
// name, each request signed, current and single.

var connectCodeShape = regexp.MustCompile(`^fxc_[A-Za-z0-9_-]{43}$`)

type connectionPayload struct {
	Store          string `json:"store"`
	InstanceID     string `json:"instance_id"`
	FilexOrigin    string `json:"filex_origin"`
	KeyFingerprint string `json:"key_fingerprint"`
	ConnectedAt    string `json:"connected_at"`
}

// filexServer is a connected filex as the store sees it: its key, the
// instance it is connected to, and a client with an address of its own.
type filexServer struct {
	s        *stack
	c        *client
	priv     ed25519.PrivateKey
	instance string
}

func newFilexServer(s *stack, ip string) *filexServer {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	c := s.newClient()
	c.xff = ip
	return &filexServer{s: s, c: c, priv: priv}
}

func (f *filexServer) publicHex() string {
	return hex.EncodeToString(f.priv.Public().(ed25519.PublicKey))
}

func (f *filexServer) fingerprint() string {
	return install.KeyFingerprint(f.priv.Public().(ed25519.PublicKey))
}

func nonce() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// signedHeaders signs one request as filex does.
func (f *filexServer) signedHeaders(method, path, instance string, body []byte, at time.Time, n string) map[string]string {
	ts := strconv.FormatInt(at.Unix(), 10)
	return map[string]string{
		install.HeaderInstance:  instance,
		install.HeaderKey:       f.fingerprint(),
		install.HeaderTimestamp: ts,
		install.HeaderNonce:     n,
		install.HeaderSignature: install.SignRequest(f.priv, method, path, instance, ts, n, body),
		"Content-Type":          "application/json",
	}
}

// send makes a signed request with a raw body.
func (f *filexServer) send(method, path string, body []byte, hdr map[string]string) resp {
	return f.c.do(method, path, rawBody(body), hdr)
}

// call signs and sends a request now, with a fresh nonce.
func (f *filexServer) call(method, path string, body []byte) resp {
	return f.send(method, path, body, f.signedHeaders(method, path, f.instance, body, time.Now(), nonce()))
}

// rawBody is a body client.do sends as it is (json.RawMessage marshals to
// itself).
func rawBody(b []byte) any {
	if b == nil {
		return nil
	}
	return json.RawMessage(b)
}

func (f *filexServer) connect(t *testing.T, code, origin string) resp {
	t.Helper()
	return f.c.do(http.MethodPost, "/v1/instances/connect", map[string]string{"code": code, "public_key": f.publicHex(),
		"filex_origin": origin, "filex_instance_id": "fx-test-1"}, nil)
}

func saveInstance(t *testing.T, c *client, url string) string {
	t.Helper()
	r := c.change(http.MethodPost, "/api/v1/me/instances", map[string]any{"url": url})
	if r.status != http.StatusCreated && r.status != http.StatusOK {
		t.Fatalf("save instance: %d %s", r.status, r.body)
	}
	var v struct {
		ID string `json:"id"`
	}
	r.json(t, &v)
	return v.ID
}

func connectCode(t *testing.T, c *client, instance string) string {
	t.Helper()
	r := c.change(http.MethodPost, "/api/v1/me/instances/"+instance+"/connect-code", nil)
	if r.status != http.StatusCreated {
		t.Fatalf("connect code: %d %s", r.status, r.body)
	}
	var v struct {
		Code      string `json:"code"`
		ExpiresAt string `json:"expires_at"`
	}
	r.json(t, &v)
	if !connectCodeShape.MatchString(v.Code) || v.ExpiresAt == "" {
		t.Fatalf("code %+v", v)
	}
	return v.Code
}

func TestAFilexConnectsWithACodeAndAsksForIntentsSigned(t *testing.T) {
	s := newStack(t)
	alice := s.signInWithRole(1, "alice", "")
	s.seedApp(t, "acme", alice.accountID(t), "hello", "1.0.0", "1.2.0")
	inst := saveInstance(t, alice, "https://files.example.com")

	// codes: the owner's, through the store's own pages
	if r := s.newClient().change(http.MethodPost, "/api/v1/me/instances/"+inst+"/connect-code", nil); r.status != http.StatusUnauthorized {
		t.Fatalf("anonymous code: %d", r.status)
	}
	if r := alice.do(http.MethodPost, "/api/v1/me/instances/"+inst+"/connect-code", nil, map[string]string{"Origin": s.srv.URL}); r.status != http.StatusForbidden {
		t.Fatalf("code without CSRF: %d", r.status)
	}
	bob := s.signInWithRole(2, "bob", "")
	if r := bob.change(http.MethodPost, "/api/v1/me/instances/"+inst+"/connect-code", nil); r.status != http.StatusNotFound || r.code(t) != "instance_not_found" {
		t.Fatalf("bob's code for alice's instance: %d %s", r.status, r.body)
	}
	code := connectCode(t, alice, inst)

	f := newFilexServer(s, "203.0.113.40")
	// another filex's address: nothing is bound, the code stays usable
	if r := f.connect(t, code, "https://other.example.com"); r.status != http.StatusConflict || r.code(t) != "wrong_instance" {
		t.Fatalf("wrong address: %d %s", r.status, r.body)
	}
	if r := f.c.do(http.MethodPost, "/v1/instances/connect", map[string]string{"code": code, "public_key": "abcd", "filex_origin": "https://files.example.com"}, nil); r.status != http.StatusBadRequest {
		t.Fatalf("a short key: %d %s", r.status, r.body)
	}
	if r := f.connect(t, "fxc_"+strings.Repeat("A", 43), "https://files.example.com"); r.status != http.StatusNotFound {
		t.Fatalf("unknown code: %d %s", r.status, r.body)
	}
	r := f.connect(t, code, "https://Files.Example.com/")
	if r.status != http.StatusOK {
		t.Fatalf("connect: %d %s", r.status, r.body)
	}
	id, pub := s.storeKey(t, "index")
	var p connectionPayload
	openEnvelope(t, r.body, id, pub, &p)
	if p.Store != s.srv.URL || p.InstanceID != inst || p.FilexOrigin != "https://files.example.com" || p.KeyFingerprint != f.fingerprint() || p.ConnectedAt == "" {
		t.Fatalf("connection %+v", p)
	}
	f.instance = p.InstanceID
	if r := f.connect(t, code, "https://files.example.com"); r.status != http.StatusGone || r.code(t) != "code_used" {
		t.Fatalf("the code again: %d %s", r.status, r.body)
	}

	// the owner sees it connected; nobody else sees it at all
	var mine struct {
		Items []struct {
			ID             string `json:"id"`
			Connected      bool   `json:"connected"`
			KeyFingerprint string `json:"key_fingerprint"`
		} `json:"items"`
	}
	alice.get("/api/v1/me/instances").json(t, &mine)
	if len(mine.Items) != 1 || !mine.Items[0].Connected || mine.Items[0].KeyFingerprint != f.fingerprint() {
		t.Fatalf("instances %+v", mine.Items)
	}

	// a signed intent: the instance's own address, read and held like any other
	body := []byte(`{"app":"hello"}`)
	path := "/v1/instances/" + inst + "/intents"
	hdr := f.signedHeaders(http.MethodPost, path, inst, body, time.Now(), nonce())
	r = f.send(http.MethodPost, path, body, hdr)
	if r.status != http.StatusCreated {
		t.Fatalf("intent: %d %s", r.status, r.body)
	}
	var created struct {
		Token, TokenID, ExpiresAt, App, Version string
	}
	var raw map[string]string
	r.json(t, &raw)
	created.Token, created.TokenID, created.ExpiresAt, created.App, created.Version = raw["token"], raw["token_id"], raw["expires_at"], raw["app"], raw["version"]
	if !tokenShape.MatchString(created.Token) || created.App != "hello" || created.Version != "1.2.0" || created.ExpiresAt == "" {
		t.Fatalf("created %+v", raw)
	}
	ip := s.readIntent(t, created.Token)
	if ip.FilexOrigin != "https://files.example.com" || ip.TokenID != created.TokenID || ip.LicenseKey != "" {
		t.Fatalf("intent payload %+v", ip)
	}

	// replayed, stale, tampered, another key, another instance's path
	if r := f.send(http.MethodPost, path, body, hdr); r.status != http.StatusUnauthorized || r.code(t) != "request_replayed" {
		t.Fatalf("replay: %d %s", r.status, r.body)
	}
	if r := f.send(http.MethodPost, path, body, f.signedHeaders(http.MethodPost, path, inst, body, time.Now().Add(-10*time.Minute), nonce())); r.status != http.StatusUnauthorized || r.code(t) != "request_expired" {
		t.Fatalf("stale: %d %s", r.status, r.body)
	}
	tampered := f.signedHeaders(http.MethodPost, path, inst, body, time.Now(), nonce())
	if r := f.send(http.MethodPost, path, []byte(`{"app":"other"}`), tampered); r.status != http.StatusUnauthorized || r.code(t) != "instance_unauthorized" {
		t.Fatalf("tampered body: %d %s", r.status, r.body)
	}
	stranger := newFilexServer(s, "203.0.113.41")
	stranger.instance = inst
	if r := stranger.call(http.MethodPost, path, body); r.status != http.StatusUnauthorized || r.code(t) != "instance_unauthorized" {
		t.Fatalf("another key: %d %s", r.status, r.body)
	}
	other := saveInstance(t, bob, "https://bob.example.com")
	otherPath := "/v1/instances/" + other + "/intents"
	if r := f.send(http.MethodPost, otherPath, body, f.signedHeaders(http.MethodPost, otherPath, inst, body, time.Now(), nonce())); r.status != http.StatusUnauthorized {
		t.Fatalf("another instance's path: %d %s", r.status, r.body)
	}
	if r := f.send(http.MethodPost, otherPath, body, f.signedHeaders(http.MethodPost, otherPath, other, body, time.Now(), nonce())); r.status != http.StatusUnauthorized {
		t.Fatalf("another instance named: %d %s", r.status, r.body)
	}
	// the key opens the instance's own doors only: not the store's pages
	if r := f.send(http.MethodGet, "/api/v1/me/instances", nil, f.signedHeaders(http.MethodGet, "/api/v1/me/instances", inst, nil, time.Now(), nonce())); r.status != http.StatusUnauthorized {
		t.Fatalf("the key on the store's own API: %d %s", r.status, r.body)
	}
	if r := f.call(http.MethodPost, path, []byte(`{"app":"hello","filex_url":"https://evil.example"}`)); r.status != http.StatusBadRequest {
		t.Fatalf("a filex_url in the body: %d %s", r.status, r.body)
	}

	// what the filex reads about itself
	self := f.call(http.MethodGet, "/v1/instances/"+inst+"/connection", nil)
	var sv map[string]string
	self.json(t, &sv)
	if self.status != http.StatusOK || sv["instance_id"] != inst || sv["filex_origin"] != "https://files.example.com" || sv["key_fingerprint"] != f.fingerprint() {
		t.Fatalf("self: %d %s", self.status, self.body)
	}

	// nothing secret is kept or logged
	for _, table := range []string{"instance_connect_codes", "saved_instances", "audit_log"} {
		text := s.tableText(t, table)
		if strings.Contains(text, code) || strings.Contains(text, code[4:]) || strings.Contains(text, "files.example.com") {
			t.Fatalf("%s holds the code or the address:\n%s", table, text)
		}
	}
	if logs := s.logs.String(); strings.Contains(logs, code[4:]) || strings.Contains(logs, created.Token) {
		t.Fatal("the server log holds the code or the token")
	}
	var actions []string
	rows, err := s.pool.Query(context.Background(), `SELECT action FROM audit_log WHERE target_id IN ($1, $2) ORDER BY id`, inst, created.TokenID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	rows.Close()
	if got := strings.Join(actions, ","); got != "instance.save,instance.connect_code,instance.connect,install_intent.create" {
		t.Fatalf("audit %s", got)
	}

	// the filex lets go: its key opens nothing any more
	if r := f.call(http.MethodDelete, "/v1/instances/"+inst+"/connection", nil); r.status != http.StatusNoContent {
		t.Fatalf("disconnect: %d %s", r.status, r.body)
	}
	if r := f.call(http.MethodPost, path, body); r.status != http.StatusUnauthorized {
		t.Fatalf("after disconnect: %d %s", r.status, r.body)
	}
	alice.get("/api/v1/me/instances").json(t, &mine)
	if mine.Items[0].Connected {
		t.Fatal("still connected after the filex let go")
	}
}

func TestTheOwnerDisconnectsAndAConnectionCodeExpires(t *testing.T) {
	s := newStack(t)
	alice := s.signInWithRole(1, "alice", "")
	s.seedApp(t, "acme", alice.accountID(t), "hello", "1.0.0")
	inst := saveInstance(t, alice, "https://files.example.com")

	late := connectCode(t, alice, inst)
	if _, err := s.pool.Exec(context.Background(), `UPDATE instance_connect_codes SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	f := newFilexServer(s, "203.0.113.50")
	if r := f.connect(t, late, "https://files.example.com"); r.status != http.StatusGone || r.code(t) != "code_expired" {
		t.Fatalf("expired code: %d %s", r.status, r.body)
	}
	if r := f.connect(t, connectCode(t, alice, inst), "https://files.example.com"); r.status != http.StatusOK {
		t.Fatalf("connect: %d %s", r.status, r.body)
	}
	f.instance = inst
	if r := f.call(http.MethodPost, "/v1/instances/"+inst+"/intents", []byte(`{"app":"hello"}`)); r.status != http.StatusCreated {
		t.Fatalf("intent: %d %s", r.status, r.body)
	}
	bob := s.signInWithRole(2, "bob", "")
	if r := bob.change(http.MethodDelete, "/api/v1/me/instances/"+inst+"/connection", nil); r.status != http.StatusNotFound {
		t.Fatalf("bob disconnects alice's instance: %d %s", r.status, r.body)
	}
	if r := alice.change(http.MethodDelete, "/api/v1/me/instances/"+inst+"/connection", nil); r.status != http.StatusNoContent {
		t.Fatalf("disconnect: %d %s", r.status, r.body)
	}
	if r := alice.change(http.MethodDelete, "/api/v1/me/instances/"+inst+"/connection", nil); r.status != http.StatusConflict || r.code(t) != "not_connected" {
		t.Fatalf("disconnect twice: %d %s", r.status, r.body)
	}
	if r := f.call(http.MethodPost, "/v1/instances/"+inst+"/intents", []byte(`{"app":"hello"}`)); r.status != http.StatusUnauthorized {
		t.Fatalf("after the owner disconnected: %d %s", r.status, r.body)
	}
	// a reconnection with a new key works; the old key stays out
	g := newFilexServer(s, "203.0.113.51")
	if r := g.connect(t, connectCode(t, alice, inst), "https://files.example.com"); r.status != http.StatusOK {
		t.Fatalf("reconnect: %d %s", r.status, r.body)
	}
	g.instance = inst
	if r := g.call(http.MethodPost, "/v1/instances/"+inst+"/intents", []byte(`{"app":"hello"}`)); r.status != http.StatusCreated {
		t.Fatalf("new key: %d %s", r.status, r.body)
	}
	if r := f.call(http.MethodPost, "/v1/instances/"+inst+"/intents", []byte(`{"app":"hello"}`)); r.status != http.StatusUnauthorized {
		t.Fatalf("old key after the new one: %d %s", r.status, r.body)
	}
}

func TestAnInstanceIntentCarriesALicenseOnlyWithASeatForIt(t *testing.T) {
	s := newStack(t)
	alice := s.signInWithRole(1, "alice", "")
	s.seedApp(t, "acme", alice.accountID(t), "hello", "1.0.0")
	if r := alice.change(http.MethodPut, "/api/v1/apps/hello/pricing", map[string]any{"model": "paid", "price_text": "$9"}); r.status != http.StatusOK {
		t.Fatal(r.status)
	}
	_, full := createLicense(t, alice, "hello", map[string]any{"licensee_name": "Buyer", "seats": 1})
	_, spare := createLicense(t, alice, "hello", map[string]any{"licensee_name": "Buyer", "seats": 2})
	// another filex holds the one seat of `full`
	if v := s.verify(t, s.newClient(), full, "hello", "fx-elsewhere"); v.Result != "valid" {
		t.Fatalf("seat: %+v", v)
	}
	inst := saveInstance(t, alice, "https://files.example.com")
	f := newFilexServer(s, "203.0.113.60")
	if r := f.connect(t, connectCode(t, alice, inst), "https://files.example.com"); r.status != http.StatusOK {
		t.Fatalf("connect: %d %s", r.status, r.body)
	}
	f.instance = inst
	path := "/v1/instances/" + inst + "/intents"
	if r := f.call(http.MethodPost, path, []byte(`{"app":"hello","license_key":"`+full+`"}`)); r.status != http.StatusBadRequest || r.code(t) != "license_invalid" {
		t.Fatalf("a key whose seat another filex holds: %d %s", r.status, r.body)
	}
	r := f.call(http.MethodPost, path, []byte(`{"app":"hello","license_key":"`+spare+`"}`))
	if r.status != http.StatusCreated {
		t.Fatalf("a key with a free seat: %d %s", r.status, r.body)
	}
	var created map[string]string
	r.json(t, &created)
	if p := s.readIntent(t, created["token"]); !p.Paid || p.LicenseKey != spare {
		t.Fatalf("payload paid %v key %q", p.Paid, p.LicenseKey)
	}
	// once this filex (fx-test-1) holds a seat of `full`, its key goes in
	if _, err := s.pool.Exec(context.Background(), `UPDATE license_activations SET instance_id = 'fx-test-1'`); err != nil {
		t.Fatal(err)
	}
	if r := f.call(http.MethodPost, path, []byte(`{"app":"hello","license_key":"`+full+`"}`)); r.status != http.StatusCreated {
		t.Fatalf("a key whose seat this filex holds: %d %s", r.status, r.body)
	}
}

func TestInstanceDoorsAreRateLimited(t *testing.T) {
	s := newStack(t)
	f := newFilexServer(s, "203.0.113.70")
	var last resp
	for i := 0; i < 11; i++ {
		last = f.connect(t, "fxc_"+strings.Repeat("B", 43), "https://files.example.com")
	}
	if last.status != http.StatusTooManyRequests {
		t.Fatalf("the 11th connection attempt in a minute: %d %s", last.status, last.body)
	}
	g := newFilexServer(s, "203.0.113.71")
	g.instance = "00000000-0000-0000-0000-000000000000"
	for i := 0; i < 61; i++ {
		last = g.call(http.MethodGet, "/v1/instances/"+g.instance+"/connection", nil)
	}
	if last.status != http.StatusTooManyRequests {
		t.Fatalf("the 61st signed request in a minute: %d %s", last.status, last.body)
	}
}
