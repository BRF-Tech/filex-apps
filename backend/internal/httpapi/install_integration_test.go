//go:build integration

package httpapi_test

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/store"
)

var tokenShape = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

type intentCreated struct {
	Token     string `json:"token"`
	MagicLink string `json:"magic_link"`
	ExpiresAt string `json:"expires_at"`
	App       string `json:"app"`
	Version   string `json:"version"`
	Instance  *struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		URL   string `json:"url"`
	} `json:"instance"`
}

type intentPayload struct {
	Store          string   `json:"store"`
	FilexOrigin    string   `json:"filex_origin"`
	TokenID        string   `json:"token_id"`
	App            string   `json:"app"`
	Kind           string   `json:"kind"`
	Version        string   `json:"version"`
	Repo           string   `json:"repo"`
	Ref            string   `json:"ref"`
	Commit         string   `json:"commit"`
	ManifestSHA256 string   `json:"manifest_sha256"`
	WasmSHA256     string   `json:"wasm_sha256"`
	UISHA256       string   `json:"ui_sha256"`
	Permissions    []string `json:"permissions"`
	FilexRange     string   `json:"filex_range"`
	Paid           bool     `json:"paid"`
	LicenseKey     string   `json:"license_key"`
	ExpiresAt      string   `json:"expires_at"`
}

func createIntent(t *testing.T, c *client, body map[string]any) intentCreated {
	t.Helper()
	r := c.change(http.MethodPost, "/api/v1/install-intents", body)
	if r.status != http.StatusCreated {
		t.Fatalf("create intent %v: %d %s", body, r.status, r.body)
	}
	var out intentCreated
	r.json(t, &out)
	return out
}

func (s *stack) readIntent(t *testing.T, token string) intentPayload {
	t.Helper()
	r := s.newClient().get("/v1/install/" + token)
	if r.status != http.StatusOK {
		t.Fatalf("read intent: %d %s", r.status, r.body)
	}
	id, pub := s.storeKey(t, "index")
	if id != s.keys.Index.ID {
		t.Fatalf("index key %s, the keyring's %s", id, s.keys.Index.ID)
	}
	var p intentPayload
	openEnvelope(t, r.body, id, pub, &p)
	return p
}

func TestInstallLinkFlow(t *testing.T) {
	s := newStack(t)
	alice := s.signInWithRole(1, "alice", "")
	app := s.seedApp(t, "acme", alice.accountID(t), "hello", "1.0.0", "1.2.0", "1.1.0")

	body := map[string]any{"app": "hello", "filex_url": "https://Files.Example.com/", "save_instance": true, "instance_label": "Office filex"}
	if r := s.newClient().change(http.MethodPost, "/api/v1/install-intents", body); r.status != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", r.status)
	}
	if r := alice.do(http.MethodPost, "/api/v1/install-intents", body, map[string]string{"Origin": s.srv.URL}); r.status != http.StatusForbidden {
		t.Fatalf("no CSRF token: %d", r.status)
	}
	for _, bad := range []map[string]any{
		{"app": "hello"},
		{"app": "hello", "filex_url": "https://f.example", "instance_id": "00000000-0000-0000-0000-000000000000"},
		{"app": "hello", "filex_url": "https://f.example/admin"},
		{"app": "hello", "filex_url": "http://f.example"},
		{"app": "hello", "filex_url": "javascript:alert(1)"},
		{"app": "hello", "instance_id": "00000000-0000-0000-0000-000000000000", "save_instance": true},
	} {
		if r := alice.change(http.MethodPost, "/api/v1/install-intents", bad); r.status != http.StatusBadRequest && r.status != http.StatusNotFound {
			t.Errorf("%v: %d %s", bad, r.status, r.body)
		}
	}
	if r := alice.change(http.MethodPost, "/api/v1/install-intents", map[string]any{"app": "nope", "filex_url": "https://f.example"}); r.status != http.StatusNotFound {
		t.Fatalf("unknown app: %d", r.status)
	}
	if r := alice.change(http.MethodPost, "/api/v1/install-intents", map[string]any{"app": "hello", "version": "9.9.9", "filex_url": "https://f.example"}); r.status != http.StatusNotFound || r.code(t) != "version_not_found" {
		t.Fatalf("unknown version: %d %s", r.status, r.body)
	}

	c := createIntent(t, alice, body)
	if !tokenShape.MatchString(c.Token) || c.App != "hello" || c.Version != "1.2.0" {
		t.Fatalf("created %+v", c)
	}
	if c.MagicLink != "https://files.example.com/admin/store-install#store="+url.QueryEscape(s.srv.URL)+"&intent="+c.Token {
		t.Fatalf("magic link %s", c.MagicLink)
	}
	if c.Instance == nil || c.Instance.URL != "https://files.example.com" || c.Instance.Label != "Office filex" {
		t.Fatalf("instance %+v", c.Instance)
	}

	// what the filex reads, signed with the index key
	p := s.readIntent(t, c.Token)
	ver, err := store.GetVersion(context.Background(), s.pool, app.ID, "1.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if p.Store != s.srv.URL || p.App != "hello" || p.Kind != "app" || p.Version != "1.2.0" || p.Repo != "acme/filex-hello" ||
		p.Ref != "v1.2.0" || p.Commit != ver.Commit || p.ManifestSHA256 != ver.ManifestSHA256 || p.WasmSHA256 != ver.Wasm.SHA256 ||
		p.UISHA256 != "" || strings.Join(p.Permissions, ",") != "files:read,net:fetch" || p.FilexRange != ">=0.50.0" ||
		p.Paid || p.LicenseKey != "" || p.ExpiresAt == "" || !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(p.TokenID) {
		t.Fatalf("payload %+v", p)
	}
	if strings.Contains(c.Token, p.TokenID) {
		t.Fatal("the token id is part of the token")
	}
	// the filex the link is for: the magic link's own origin, normalized
	if p.FilexOrigin != "https://files.example.com" || !strings.HasPrefix(c.MagicLink, p.FilexOrigin+"/admin/store-install#") {
		t.Fatalf("filex_origin %q, link %s", p.FilexOrigin, c.MagicLink)
	}
	// commit and manifest_sha256 are always in the signed bytes (filex requires both)
	var raw struct {
		Payload map[string]any `json:"payload"`
	}
	s.newClient().get("/v1/install/"+c.Token).json(t, &raw)
	for _, k := range []string{"commit", "manifest_sha256", "filex_origin", "store", "token_id"} {
		if v, ok := raw.Payload[k].(string); !ok || v == "" {
			t.Fatalf("payload %s = %v", k, raw.Payload[k])
		}
	}
	// readable until it is used
	s.readIntent(t, c.Token)

	// neither the token nor the address is in the database or the log
	for _, table := range []string{"install_intents", "saved_instances", "audit_log"} {
		text := s.tableText(t, table)
		for _, secret := range []string{c.Token, "files.example.com", "Office filex"} {
			if strings.Contains(text, secret) {
				t.Fatalf("%s holds %q:\n%s", table, secret, text)
			}
		}
	}
	rawCols := string(s.rawBytes(t, "saved_instances", "url_index", "url_sealed", "label_sealed")) +
		string(s.rawBytes(t, "install_intents", "token_hash", "filex_origin_sealed"))
	for _, secret := range []string{c.Token, "files.example.com", "Office filex"} {
		if strings.Contains(rawCols, secret) {
			t.Fatalf("a binary column holds %q in the clear", secret)
		}
	}
	if logs := s.logs.String(); strings.Contains(logs, c.Token) || strings.Contains(logs, "files.example.com") {
		t.Fatal("the server log holds the token or the address")
	}

	// complete: once
	filex := s.newClient()
	done := filex.do(http.MethodPost, "/v1/install/"+c.Token+"/complete", map[string]string{"instance_id": "inst-1", "result": "installed"}, nil)
	if done.status != http.StatusOK || !strings.Contains(string(done.body), `"result":"installed"`) || !strings.Contains(string(done.body), p.TokenID) {
		t.Fatalf("complete: %d %s", done.status, done.body)
	}
	if r := filex.do(http.MethodPost, "/v1/install/"+c.Token+"/complete", map[string]string{"instance_id": "inst-1", "result": "cancelled"}, nil); r.status != http.StatusGone || r.code(t) != "intent_used" {
		t.Fatalf("complete twice: %d %s", r.status, r.body)
	}
	if r := filex.get("/v1/install/" + c.Token); r.status != http.StatusGone || r.code(t) != "intent_used" {
		t.Fatalf("read after use: %d %s", r.status, r.body)
	}
	if r := filex.get("/v1/install/" + strings.Repeat("Z", 43)); r.status != http.StatusNotFound {
		t.Fatalf("unknown token: %d", r.status)
	}
	if r := filex.get("/v1/install/short"); r.status != http.StatusNotFound {
		t.Fatalf("malformed token: %d", r.status)
	}

	// a saved instance: its owner's only
	var mine struct {
		Items []struct{ ID, Label, URL string } `json:"items"`
	}
	alice.get("/api/v1/me/instances").json(t, &mine)
	if len(mine.Items) != 1 || mine.Items[0].URL != "https://files.example.com" {
		t.Fatalf("instances %+v", mine.Items)
	}
	again := createIntent(t, alice, map[string]any{"app": "hello", "version": "1.0.0", "instance_id": c.Instance.ID})
	if !strings.HasPrefix(again.MagicLink, "https://files.example.com/admin/store-install#") || again.Version != "1.0.0" {
		t.Fatalf("by saved instance %+v", again)
	}
	if ap := s.readIntent(t, again.Token); ap.FilexOrigin != "https://files.example.com" || ap.Version != "1.0.0" {
		t.Fatalf("by saved instance: filex_origin %q version %s", ap.FilexOrigin, ap.Version)
	}
	port := createIntent(t, alice, map[string]any{"app": "hello", "filex_url": "https://Files.Example.com:8443/"})
	if pp := s.readIntent(t, port.Token); pp.FilexOrigin != "https://files.example.com:8443" ||
		!strings.HasPrefix(port.MagicLink, pp.FilexOrigin+"/admin/store-install#") {
		t.Fatalf("with a port: filex_origin %q, link %s", pp.FilexOrigin, port.MagicLink)
	}
	// a sealed origin moved to another intent's row does not open there
	if _, err := s.pool.Exec(context.Background(), `UPDATE install_intents SET filex_origin_sealed =
		(SELECT filex_origin_sealed FROM install_intents WHERE token_hash = sha256(convert_to($1, 'UTF8')))
		WHERE token_hash = sha256(convert_to($2, 'UTF8'))`, port.Token, again.Token); err != nil {
		t.Fatal(err)
	}
	if r := s.newClient().get("/v1/install/" + again.Token); r.status != http.StatusInternalServerError {
		t.Fatalf("a moved filex_origin opened: %d %s", r.status, r.body)
	}
	if r := alice.change(http.MethodPost, "/api/v1/me/instances", map[string]any{"url": "https://files.example.com:443"}); r.status != http.StatusOK {
		t.Fatalf("saving the same address again: %d %s", r.status, r.body)
	}
	bob := s.signInWithRole(2, "bob", "")
	bob.get("/api/v1/me/instances").json(t, &mine)
	if len(mine.Items) != 0 {
		t.Fatalf("bob sees %+v", mine.Items)
	}
	if r := bob.change(http.MethodPost, "/api/v1/install-intents", map[string]any{"app": "hello", "instance_id": c.Instance.ID}); r.status != http.StatusNotFound || r.code(t) != "instance_not_found" {
		t.Fatalf("bob uses alice's instance: %d %s", r.status, r.body)
	}
	if r := bob.change(http.MethodDelete, "/api/v1/me/instances/"+c.Instance.ID, nil); r.status != http.StatusNotFound {
		t.Fatalf("bob deletes alice's instance: %d", r.status)
	}
	if r := bob.change(http.MethodPost, "/api/v1/me/instances", map[string]any{"url": "https://files.example.com"}); r.status != http.StatusCreated {
		t.Fatalf("bob saves the same address: %d %s", r.status, r.body)
	}
	if r := alice.change(http.MethodDelete, "/api/v1/me/instances/"+c.Instance.ID, nil); r.status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	alice.get("/api/v1/me/instances").json(t, &mine)
	if len(mine.Items) != 0 {
		t.Fatalf("after delete %+v", mine.Items)
	}
}

func TestInstallLinkExpiresAndRefusesWhatCannotBeInstalled(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	alice := s.signInWithRole(1, "alice", "")
	app := s.seedApp(t, "acme", alice.accountID(t), "hello", "1.0.0", "2.0.0")

	c := createIntent(t, alice, map[string]any{"app": "hello", "filex_url": "https://f.example"})
	if _, err := s.pool.Exec(ctx, `UPDATE install_intents SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	filex := s.newClient()
	if r := filex.get("/v1/install/" + c.Token); r.status != http.StatusGone || r.code(t) != "intent_expired" {
		t.Fatalf("expired read: %d %s", r.status, r.body)
	}
	if r := filex.do(http.MethodPost, "/v1/install/"+c.Token+"/complete", map[string]string{"instance_id": "i", "result": "installed"}, nil); r.status != http.StatusGone || r.code(t) != "intent_expired" {
		t.Fatalf("expired complete: %d %s", r.status, r.body)
	}

	ver2, _ := store.GetVersion(ctx, s.pool, app.ID, "2.0.0")
	if err := store.YankVersion(ctx, s.pool, ver2.ID, "broken", alice.accountID(t)); err != nil {
		t.Fatal(err)
	}
	if r := alice.change(http.MethodPost, "/api/v1/install-intents", map[string]any{"app": "hello", "version": "2.0.0", "filex_url": "https://f.example"}); r.status != http.StatusConflict || r.code(t) != "version_yanked" {
		t.Fatalf("yanked version: %d %s", r.status, r.body)
	}
	if got := createIntent(t, alice, map[string]any{"app": "hello", "filex_url": "https://f.example"}); got.Version != "1.0.0" {
		t.Fatalf("the default skips the yanked version: %+v", got)
	}
	live := createIntent(t, alice, map[string]any{"app": "hello", "filex_url": "https://f.example"})
	if err := store.RevokeApp(ctx, s.pool, app.ID, "harmful"); err != nil {
		t.Fatal(err)
	}
	if r := alice.change(http.MethodPost, "/api/v1/install-intents", map[string]any{"app": "hello", "filex_url": "https://f.example"}); r.status != http.StatusConflict || r.code(t) != "app_revoked" {
		t.Fatalf("revoked app: %d %s", r.status, r.body)
	}
	if r := filex.get("/v1/install/" + live.Token); r.status != http.StatusConflict || r.code(t) != "app_revoked" {
		t.Fatalf("a link to an app revoked since: %d %s", r.status, r.body)
	}
}

func TestInstallLinkCarriesALicenceKeySealed(t *testing.T) {
	s := newStack(t)
	alice := s.signInWithRole(1, "alice", "")
	s.seedApp(t, "acme", alice.accountID(t), "hello", "1.0.0")
	s.seedApp(t, "acme", alice.accountID(t), "other", "1.0.0")
	if r := alice.change(http.MethodPut, "/api/v1/apps/hello/pricing", map[string]any{"model": "paid", "price_text": "$9"}); r.status != http.StatusOK {
		t.Fatal(r.status)
	}
	_, key := createLicense(t, alice, "hello", map[string]any{"licensee_name": "Buyer"})
	_, otherKey := createLicense(t, alice, "other", map[string]any{"licensee_name": "Buyer"})

	if r := alice.change(http.MethodPost, "/api/v1/install-intents", map[string]any{"app": "hello", "filex_url": "https://f.example", "license_key": otherKey}); r.status != http.StatusBadRequest || r.code(t) != "license_invalid" {
		t.Fatalf("another app's key: %d %s", r.status, r.body)
	}
	if r := alice.change(http.MethodPost, "/api/v1/install-intents", map[string]any{"app": "hello", "filex_url": "https://f.example", "license_key": "FXL-0000"}); r.status != http.StatusBadRequest || r.code(t) != "license_invalid" {
		t.Fatalf("a malformed key: %d %s", r.status, r.body)
	}
	c := createIntent(t, alice, map[string]any{"app": "hello", "filex_url": "https://f.example", "license_key": strings.ToLower(key)})
	text := s.tableText(t, "install_intents") + string(s.rawBytes(t, "install_intents", "license_key_sealed", "token_hash"))
	if strings.Contains(text, key) || strings.Contains(text, key[9:]) || strings.Contains(text, strings.ToLower(key[9:])) {
		t.Fatalf("the intent holds the license key in the clear:\n%s", text)
	}
	p := s.readIntent(t, c.Token)
	if !p.Paid || p.LicenseKey != key {
		t.Fatalf("payload paid %v key %q, want %q", p.Paid, p.LicenseKey, key)
	}
	var sealed int
	_ = s.pool.QueryRow(context.Background(), `SELECT count(*) FROM install_intents WHERE license_key_sealed IS NOT NULL`).Scan(&sealed)
	if sealed != 1 {
		t.Fatalf("%d sealed keys, want 1", sealed)
	}
	if r := s.newClient().do(http.MethodPost, "/v1/install/"+c.Token+"/complete", map[string]string{"instance_id": "inst-9", "result": "installed"}, nil); r.status != http.StatusOK {
		t.Fatalf("complete: %d %s", r.status, r.body)
	}
	_ = s.pool.QueryRow(context.Background(), `SELECT count(*) FROM install_intents WHERE license_key_sealed IS NOT NULL`).Scan(&sealed)
	if sealed != 0 {
		t.Fatal("the sealed key outlived its intent")
	}
	if logs := s.logs.String(); strings.Contains(logs, key) || strings.Contains(logs, key[9:]) {
		t.Fatal("the server log holds the key")
	}
}
