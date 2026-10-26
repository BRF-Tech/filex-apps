//go:build integration

package httpapi_test

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/signedjson"
	"github.com/brf-tech/filex-apps/backend/internal/signing"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

var keyShape = regexp.MustCompile(`^FXL-[0-9A-HJKMNP-TV-Z]{4}(-[0-9A-HJKMNP-TV-Z]{4}){5}-[0-9A-HJKMNP-TV-Z]{2}$`)

// accountID is the signed-in browser's account.
func (c *client) accountID(t *testing.T) int64 {
	t.Helper()
	var m struct {
		Account struct {
			ID int64 `json:"id"`
		} `json:"account"`
	}
	c.get("/api/v1/me").json(t, &m)
	if m.Account.ID == 0 {
		t.Fatal("no account")
	}
	return m.Account.ID
}

// seedApp makes an active publisher (owner: account), an app of it and its
// approved versions, without the pipeline.
func (s *stack) seedApp(t *testing.T, publisher string, owner int64, name string, versions ...string) store.App {
	t.Helper()
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO publishers (id, name, github_owner, github_owner_id, owner_type, status)
		VALUES ($1, $1, $1, (SELECT coalesce(max(github_owner_id), 500) + 1 FROM publishers), 'Organization', 'active')
		ON CONFLICT (id) DO NOTHING`, publisher); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO publisher_members (publisher_id, account_id, role) VALUES ($1, $2, 'owner')
		ON CONFLICT DO NOTHING`, publisher, owner); err != nil {
		t.Fatal(err)
	}
	var repoID int64
	_ = s.pool.QueryRow(ctx, `SELECT coalesce(max(repo_id), 7000) + 1 FROM apps`).Scan(&repoID)
	id, err := store.InsertApp(ctx, s.pool, store.App{Name: name, Kind: "app", PublisherID: publisher, RepoOwner: publisher,
		RepoName: "filex-" + name, RepoID: repoID, Label: map[string]string{"en": "App " + name}})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range versions {
		sub, err := store.InsertSubmission(ctx, s.pool, publisher, owner, publisher+"/filex-"+name, "v"+v, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.pool.Exec(ctx, `UPDATE submissions SET status = 'approved', app_id = $2 WHERE id = $1`, sub, id); err != nil {
			t.Fatal(err)
		}
		wasm := sha([]byte(name + v))
		if _, err := store.InsertVersion(ctx, s.pool, store.Version{AppID: id, SubmissionID: sub, Version: v, Ref: "v" + v,
			Commit: strings.Repeat("c", 40), FilexRange: ">=0.50.0", ManifestURL: "https://raw.githubusercontent.com/m.json",
			ManifestSHA256: sha([]byte("manifest " + name + v)), ManifestSig: "00",
			Wasm:        &store.Artifact{URL: "https://github.com/x.wasm", SHA256: wasm, Size: 10, Sig: "00"},
			Permissions: []string{"files:read", "net:fetch"}, ArtifactKeyID: s.keys.Artifact.ID}); err != nil {
			t.Fatal(err)
		}
	}
	app, err := store.GetAppByID(ctx, s.pool, id)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

// storeKeys reads /v1/keys.json and returns the active key of a use.
func (s *stack) storeKey(t *testing.T, use string) (string, ed25519.PublicKey) {
	t.Helper()
	var ks struct {
		Keys []struct {
			ID, Use, Ed25519, Status string
		} `json:"keys"`
	}
	s.newClient().get("/v1/keys.json").json(t, &ks)
	for _, k := range ks.Keys {
		if k.Use == use && k.Status == "active" {
			pk, err := signing.ParsePublicKey(k.Ed25519)
			if err != nil {
				t.Fatal(err)
			}
			return k.ID, pk
		}
	}
	t.Fatalf("no active %s key in /v1/keys.json: %+v", use, ks.Keys)
	return "", nil
}

// openEnvelope checks a signed answer twice: with the store's own verifier
// and with a separate one written here (sorted-key re-encoding by hand,
// crypto/ed25519 directly).
func openEnvelope(t *testing.T, body []byte, keyID string, pub ed25519.PublicKey, into any) {
	t.Helper()
	var env domain.SignedEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("not an envelope: %s", body)
	}
	if env.KeyID != keyID {
		t.Fatalf("key_id %q, want %q", env.KeyID, keyID)
	}
	if _, err := signedjson.Verify([]ed25519.PublicKey{pub}, env); err != nil {
		t.Fatalf("envelope: %v\n%s", err, body)
	}
	var tree map[string]any
	if err := json.Unmarshal(env.Payload, &tree); err != nil {
		t.Fatal(err)
	}
	canon := handCanonical(tree)
	sum := sha256.Sum256([]byte(canon))
	sig, _ := hex.DecodeString(env.Signature)
	if !ed25519.Verify(pub, []byte(hex.EncodeToString(sum[:])), sig) {
		t.Fatalf("an independent verifier refuses the envelope; its canonical bytes:\n%s", canon)
	}
	for k, v := range tree {
		if v == nil {
			t.Fatalf("payload field %q is null (absent is the rule)", k)
		}
	}
	if err := json.Unmarshal(env.Payload, into); err != nil {
		t.Fatal(err)
	}
}

// handCanonical writes a decoded JSON tree with sorted keys and no space,
// without encoding/json's map ordering (strings through strconv-style
// quoting of the few characters a payload may hold).
func handCanonical(v any) string {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, quote(k)+":"+handCanonical(x[k]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	case []any:
		parts := make([]string, 0, len(x))
		for _, e := range x {
			parts = append(parts, handCanonical(e))
		}
		return "[" + strings.Join(parts, ",") + "]"
	case string:
		return quote(x)
	case bool:
		return fmt.Sprint(x)
	case float64:
		return fmt.Sprint(int64(x))
	}
	return "null"
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

type verdict struct {
	Result       string `json:"result"`
	App          string `json:"app"`
	Licensee     string `json:"licensee"`
	Seats        int    `json:"seats"`
	SeatsUsed    *int   `json:"seats_used"`
	ValidUntil   string `json:"valid_until"`
	UpdatesUntil string `json:"updates_until"`
	InstanceID   string `json:"instance_id"`
	CheckedAt    string `json:"checked_at"`
	NextCheckBy  string `json:"next_check_by"`
	GraceUntil   string `json:"grace_until"`
}

func (s *stack) verify(t *testing.T, c *client, key, app, instance string) verdict {
	t.Helper()
	r := c.do(http.MethodPost, "/v1/licenses/verify", map[string]string{"key": key, "app": app, "instance_id": instance,
		"filex_version": "v0.51.0 (abc, 2026-10-04)"}, nil)
	if r.status != http.StatusOK {
		t.Fatalf("verify: %d %s", r.status, r.body)
	}
	id, pub := s.storeKey(t, "license")
	var v verdict
	openEnvelope(t, r.body, id, pub, &v)
	if v.App != app || v.InstanceID != instance || v.CheckedAt == "" || v.NextCheckBy <= v.CheckedAt || v.GraceUntil <= v.NextCheckBy {
		t.Fatalf("verdict %+v", v)
	}
	return v
}

type licenseBody struct {
	ID           string `json:"id"`
	App          string `json:"app"`
	KeyPrefix    string `json:"key_prefix"`
	LicenseeName string `json:"licensee_name"`
	Seats        int    `json:"seats"`
	SeatsUsed    int    `json:"seats_used"`
	ValidUntil   string `json:"valid_until"`
	Status       string `json:"status"`
	CreatedBy    string `json:"created_by"`
	RevokedAt    string `json:"revoked_at"`
}

func createLicense(t *testing.T, c *client, app string, body map[string]any) (licenseBody, string) {
	t.Helper()
	r := c.change(http.MethodPost, "/api/v1/apps/"+app+"/licenses", body)
	if r.status != http.StatusCreated {
		t.Fatalf("create license: %d %s", r.status, r.body)
	}
	var out struct {
		License licenseBody `json:"license"`
		Key     string      `json:"key"`
	}
	r.json(t, &out)
	if !keyShape.MatchString(out.Key) || out.License.KeyPrefix != out.Key[:8] {
		t.Fatalf("key %q prefix %q", out.Key, out.License.KeyPrefix)
	}
	return out.License, out.Key
}

// tableText is every row of a table as JSON text: what someone reading the
// database would see.
func (s *stack) tableText(t *testing.T, table string) string {
	t.Helper()
	var out string
	if err := s.pool.QueryRow(context.Background(),
		`SELECT coalesce(string_agg(row_to_json(x)::text, E'\n'), '') FROM `+table+` x`).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// rawBytes is the raw content of a table's binary columns (row_to_json
// shows bytea as hex, which would hide a value stored in the clear).
func (s *stack) rawBytes(t *testing.T, table string, cols ...string) []byte {
	t.Helper()
	rows, err := s.pool.Query(context.Background(), `SELECT `+strings.Join(cols, ", ")+` FROM `+table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []byte
	for rows.Next() {
		vals := make([][]byte, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		for _, v := range vals {
			out = append(append(out, v...), ' ')
		}
	}
	return out
}

func TestPricingIsSetByThePublishersMembersAndAdministrators(t *testing.T) {
	s := newStack(t)
	alice := s.signInWithRole(1, "alice", "")
	s.seedApp(t, "acme", alice.accountID(t), "hello", "1.0.0")
	paid := map[string]any{"model": "paid", "price_text": "$29 per instance", "purchase_url": "https://acme.example/buy"}

	if r := s.newClient().change(http.MethodPut, "/api/v1/apps/hello/pricing", paid); r.status != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", r.status)
	}
	bob := s.signInWithRole(2, "bob", "")
	if r := bob.change(http.MethodPut, "/api/v1/apps/hello/pricing", paid); r.status != http.StatusForbidden || r.code(t) != "not_a_member" {
		t.Fatalf("not a member: %d %s", r.status, r.body)
	}
	reviewer := s.signInWithRole(3, "rev", "triage")
	if r := reviewer.change(http.MethodPut, "/api/v1/apps/hello/pricing", paid); r.status != http.StatusForbidden {
		t.Fatalf("reviewer: %d %s", r.status, r.body)
	}
	if r := alice.do(http.MethodPut, "/api/v1/apps/hello/pricing", paid, map[string]string{"Origin": s.srv.URL}); r.status != http.StatusForbidden || r.code(t) != "csrf_failed" {
		t.Fatalf("no CSRF token: %d %s", r.status, r.body)
	}
	if r := alice.change(http.MethodPut, "/api/v1/apps/hello/pricing", map[string]any{"model": "free", "price_text": "$0"}); r.status != http.StatusBadRequest {
		t.Fatalf("free with a price: %d %s", r.status, r.body)
	}
	if r := alice.change(http.MethodPut, "/api/v1/apps/hello/pricing", paid); r.status != http.StatusOK {
		t.Fatalf("member: %d %s", r.status, r.body)
	}
	var app struct {
		Pricing domain.Pricing `json:"pricing"`
	}
	s.newClient().get("/api/v1/apps/hello").json(t, &app)
	if app.Pricing.Model != "paid" || app.Pricing.PriceText != "$29 per instance" || app.Pricing.PurchaseURL != "https://acme.example/buy" {
		t.Fatalf("app page pricing %+v", app.Pricing)
	}
	var list struct {
		Items []struct {
			Name    string         `json:"name"`
			Pricing domain.Pricing `json:"pricing"`
		} `json:"items"`
	}
	s.newClient().get("/api/v1/apps").json(t, &list)
	if len(list.Items) != 1 || list.Items[0].Pricing.Model != "paid" {
		t.Fatalf("catalog pricing %+v", list.Items)
	}
	admin := s.signInWithRole(4, "adm", "write")
	if r := admin.change(http.MethodPut, "/api/v1/apps/hello/pricing", map[string]any{"model": "free"}); r.status != http.StatusOK {
		t.Fatalf("admin: %d %s", r.status, r.body)
	}
	app.Pricing = domain.Pricing{}
	s.newClient().get("/api/v1/apps/hello").json(t, &app)
	if app.Pricing.Model != "free" || app.Pricing.PriceText != "" {
		t.Fatalf("after admin %+v", app.Pricing)
	}
	var mine struct {
		Items []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"items"`
	}
	alice.get("/api/v1/me/apps").json(t, &mine)
	if len(mine.Items) != 1 || mine.Items[0].Name != "hello" {
		t.Fatalf("my apps %+v", mine.Items)
	}
	bob.get("/api/v1/me/apps").json(t, &mine)
	if len(mine.Items) != 0 {
		t.Fatalf("bob's apps %+v", mine.Items)
	}
}

func TestLicenceLifecycleAndTheSignedCheck(t *testing.T) {
	s := newStack(t)
	alice := s.signInWithRole(1, "alice", "")
	s.seedApp(t, "acme", alice.accountID(t), "hello", "1.0.0")
	s.seedApp(t, "acme", alice.accountID(t), "other", "1.0.0")
	filexA := s.newClient()
	filexA.xff = "203.0.113.10"

	if r := s.newClient().change(http.MethodPost, "/api/v1/apps/hello/licenses", map[string]any{"licensee_name": "X"}); r.status != http.StatusUnauthorized {
		t.Fatalf("anonymous create: %d", r.status)
	}
	bob := s.signInWithRole(2, "bob", "")
	if r := bob.change(http.MethodPost, "/api/v1/apps/hello/licenses", map[string]any{"licensee_name": "X"}); r.status != http.StatusForbidden {
		t.Fatalf("bob create: %d %s", r.status, r.body)
	}
	if r := bob.get("/api/v1/apps/hello/licenses"); r.status != http.StatusForbidden {
		t.Fatalf("bob list: %d %s", r.status, r.body)
	}
	for _, bad := range []map[string]any{
		{}, {"licensee_name": "X", "seats": 0}, {"licensee_name": "X", "valid_until": "2001-01-01"},
		{"licensee_name": "X", "valid_until": "31.12.2099"}, {"licensee_name": "a b"},
		{"licensee_name": "X", "licensee_contact": "not a contact!"}, {"licensee_name": "X", "key": "FXL-AAAA"},
	} {
		if r := alice.change(http.MethodPost, "/api/v1/apps/hello/licenses", bad); r.status != http.StatusBadRequest {
			t.Errorf("%v: %d %s", bad, r.status, r.body)
		}
	}

	lic, key := createLicense(t, alice, "hello", map[string]any{"licensee_name": "Zoë Ångström & Co <legal>", "licensee_contact": "legal@acme.example",
		"seats": 1, "note": "first customer"})
	if lic.Seats != 1 || lic.Status != "active" || lic.CreatedBy != "alice" || lic.SeatsUsed != 0 {
		t.Fatalf("license %+v", lic)
	}
	// The key is in that answer only: not in the list, not in any table, not
	// in the audit log, not in the server's log.
	var page struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	lr := alice.get("/api/v1/apps/hello/licenses")
	lr.json(t, &page)
	if page.Total != 1 || strings.Contains(string(lr.body), key) || page.Items[0]["key"] != nil {
		t.Fatalf("list: %s", lr.body)
	}
	tail := strings.ReplaceAll(key[4:], "-", "")
	for _, table := range []string{"licenses", "audit_log", "license_activations"} {
		text := s.tableText(t, table)
		if strings.Contains(text, key) || strings.Contains(text, tail) || strings.Contains(text, key[9:]) {
			t.Fatalf("%s holds the key:\n%s", table, text)
		}
	}
	if raw := string(s.rawBytes(t, "licenses", "key_hash")); strings.Contains(raw, key[9:]) || strings.Contains(raw, tail) {
		t.Fatal("key_hash holds the key")
	}

	// valid: the first instance takes the one seat
	v := s.verify(t, filexA, key, "hello", "inst-A")
	if v.Result != "valid" || v.Licensee != "Zoë Ångström & Co <legal>" || v.Seats != 1 || v.SeatsUsed == nil || *v.SeatsUsed != 1 || v.ValidUntil != "" {
		t.Fatalf("first check %+v", v)
	}
	// the same instance again: still valid, no second seat
	if v := s.verify(t, filexA, strings.ToLower(key), "hello", "inst-A"); v.Result != "valid" || *v.SeatsUsed != 1 {
		t.Fatalf("again %+v", v)
	}
	// a second instance: no seat left
	if v := s.verify(t, filexA, key, "hello", "inst-B"); v.Result != "seats_exhausted" || *v.SeatsUsed != 1 {
		t.Fatalf("second instance %+v", v)
	}
	// release the first: the second takes the seat
	var acts struct {
		Items []struct {
			InstanceID   string `json:"instance_id"`
			Active       bool   `json:"active"`
			FilexVersion string `json:"filex_version"`
		} `json:"items"`
	}
	alice.get("/api/v1/licenses/"+lic.ID+"/activations").json(t, &acts)
	if len(acts.Items) != 1 || acts.Items[0].InstanceID != "inst-A" || !acts.Items[0].Active || acts.Items[0].FilexVersion == "" {
		t.Fatalf("activations %+v", acts.Items)
	}
	if r := bob.change(http.MethodPost, "/api/v1/licenses/"+lic.ID+"/activations/inst-A/release", nil); r.status != http.StatusForbidden {
		t.Fatalf("bob release: %d", r.status)
	}
	if r := alice.change(http.MethodPost, "/api/v1/licenses/"+lic.ID+"/activations/inst-A/release", nil); r.status != http.StatusOK {
		t.Fatalf("release: %d %s", r.status, r.body)
	}
	if r := alice.change(http.MethodPost, "/api/v1/licenses/"+lic.ID+"/activations/inst-A/release", nil); r.status != http.StatusConflict {
		t.Fatalf("release twice: %d %s", r.status, r.body)
	}
	if v := s.verify(t, filexA, key, "hello", "inst-B"); v.Result != "valid" || *v.SeatsUsed != 1 {
		t.Fatalf("after release %+v", v)
	}
	if v := s.verify(t, filexA, key, "hello", "inst-A"); v.Result != "seats_exhausted" {
		t.Fatalf("the released instance came back without a seat: %+v", v)
	}

	// another app's key, an unknown key, a malformed key: nothing about the license
	if v := s.verify(t, filexA, key, "other", "inst-B"); v.Result != "wrong_app" || v.Licensee != "" || v.SeatsUsed != nil || v.Seats != 0 {
		t.Fatalf("wrong app %+v", v)
	}
	for _, k := range []string{"FXL-0000-0000-0000-0000-0000-0000-00", "nonsense", ""} {
		if v := s.verify(t, filexA, k, "hello", "inst-B"); v.Result != "invalid" || v.Licensee != "" || v.SeatsUsed != nil {
			t.Fatalf("%q: %+v", k, v)
		}
	}
	if r := filexA.do(http.MethodPost, "/v1/licenses/verify", map[string]string{"key": key, "app": "Bad App", "instance_id": "x"}, nil); r.status != http.StatusBadRequest {
		t.Fatalf("bad app name: %d", r.status)
	}
	if r := filexA.do(http.MethodPost, "/v1/licenses/verify", map[string]string{"key": key, "app": "hello", "instance_id": "has space"}, nil); r.status != http.StatusBadRequest {
		t.Fatalf("bad instance id: %d", r.status)
	}

	// expired: a license whose last day has passed
	exp, expKey := createLicense(t, alice, "hello", map[string]any{"licensee_name": "Expiring", "seats": 5,
		"valid_until": "2099-12-31", "updates_until": "2099-06-30"})
	if v := s.verify(t, filexA, expKey, "hello", "inst-E"); v.Result != "valid" || v.ValidUntil != "2099-12-31" || v.UpdatesUntil != "2099-06-30" {
		t.Fatalf("before expiry %+v", v)
	}
	if _, err := s.pool.Exec(context.Background(), `UPDATE licenses SET valid_until = current_date - 1 WHERE id = $1::uuid`, exp.ID); err != nil {
		t.Fatal(err)
	}
	if v := s.verify(t, filexA, expKey, "hello", "inst-E"); v.Result != "expired" || v.Licensee != "Expiring" {
		t.Fatalf("after expiry %+v", v)
	}

	// revoked
	if r := bob.change(http.MethodPost, "/api/v1/licenses/"+lic.ID+"/revoke", map[string]any{}); r.status != http.StatusForbidden {
		t.Fatalf("bob revoke: %d", r.status)
	}
	rr := alice.change(http.MethodPost, "/api/v1/licenses/"+lic.ID+"/revoke", map[string]any{"reason": "refunded"})
	var revoked licenseBody
	rr.json(t, &revoked)
	if rr.status != http.StatusOK || revoked.Status != "revoked" || revoked.RevokedAt == "" {
		t.Fatalf("revoke: %d %s", rr.status, rr.body)
	}
	if r := alice.change(http.MethodPost, "/api/v1/licenses/"+lic.ID+"/revoke", nil); r.status != http.StatusConflict {
		t.Fatalf("revoke twice: %d", r.status)
	}
	if v := s.verify(t, filexA, key, "hello", "inst-B"); v.Result != "revoked" {
		t.Fatalf("after revoke %+v", v)
	}

	// the license key is published in /v1/keys.json, never in the index
	if _, err := s.pub.EnsureFresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	idx := s.newClient().get("/v1/index.json")
	if idx.status != http.StatusOK || strings.Contains(string(idx.body), `"license"`) || !strings.Contains(string(idx.body), `"artifact"`) {
		t.Fatalf("index keys: %d %s", idx.status, idx.body)
	}

	// an administrator manages any app's licenses
	admin := s.signInWithRole(4, "adm", "maintain")
	if r := admin.get("/api/v1/apps/hello/licenses"); r.status != http.StatusOK {
		t.Fatalf("admin list: %d", r.status)
	}
	createLicense(t, admin, "other", map[string]any{"licensee_name": "By the admin"})

	// the audit log names licenses by id and prefix
	text := s.tableText(t, "audit_log")
	for _, action := range []string{"license.create", "license.activate", "license.release", "license.check_refused", "license.revoke"} {
		if !strings.Contains(text, action) {
			t.Errorf("audit log has no %s", action)
		}
	}
	if !strings.Contains(text, lic.KeyPrefix) {
		t.Error("audit log does not name the key prefix")
	}
	for _, k := range []string{key, expKey} {
		if logs := s.logs.String(); strings.Contains(logs, k) || strings.Contains(logs, k[9:]) {
			t.Fatalf("the server log holds a key")
		}
	}
}

func TestLicenceChecksAreRateLimitedPerAddress(t *testing.T) {
	s := newStack(t)
	c := s.newClient()
	c.xff = "198.51.100.77"
	body := map[string]string{"key": "nonsense", "app": "hello", "instance_id": "i"}
	for i := 0; i < 30; i++ {
		if r := c.do(http.MethodPost, "/v1/licenses/verify", body, nil); r.status != http.StatusOK {
			t.Fatalf("check %d: %d %s", i, r.status, r.body)
		}
	}
	r := c.do(http.MethodPost, "/v1/licenses/verify", body, nil)
	if r.status != http.StatusTooManyRequests || r.header.Get("Retry-After") == "" {
		t.Fatalf("31st check: %d", r.status)
	}
	other := s.newClient()
	other.xff = "198.51.100.78"
	if r := other.do(http.MethodPost, "/v1/licenses/verify", body, nil); r.status != http.StatusOK {
		t.Fatalf("another address: %d", r.status)
	}
}

func TestWithoutTheDataKeyTheFeaturesSayWhy(t *testing.T) {
	s := newStackOpts(t, stackOpts{seesAdminRepo: true, noDataKey: true})
	alice := s.signInWithRole(1, "alice", "")
	s.seedApp(t, "acme", alice.accountID(t), "hello", "1.0.0")
	for _, r := range []resp{
		s.newClient().do(http.MethodPost, "/v1/licenses/verify", map[string]string{"key": "x", "app": "hello", "instance_id": "i"}, nil),
		alice.change(http.MethodPost, "/api/v1/apps/hello/licenses", map[string]any{"licensee_name": "X"}),
		alice.change(http.MethodPost, "/api/v1/install-intents", map[string]any{"app": "hello", "filex_url": "https://f.example"}),
		alice.get("/api/v1/me/instances"),
	} {
		if r.status != http.StatusServiceUnavailable || r.code(t) != "data_key_missing" {
			t.Fatalf("%d %s", r.status, r.body)
		}
	}
	var ready struct {
		Warnings []string `json:"warnings"`
	}
	s.newClient().get("/readyz").json(t, &ready)
	if len(ready.Warnings) != 1 || !strings.Contains(ready.Warnings[0], "FAPPS_DATA_KEY is not set") {
		t.Fatalf("readyz warnings %q", ready.Warnings)
	}
}
