//go:build integration

package httpapi_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/notify"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// hookReceiver is a webhook receiver on this machine (the stack's notifier
// runs with APP_ENV=development: plain http and loopback are allowed).
type hookReceiver struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []hookHit
	// fail answers this many requests to a path with 503 first.
	fail map[string]int
}

type hookHit struct {
	path   string
	query  string
	header http.Header
	body   []byte
}

func newHookReceiver(t *testing.T) *hookReceiver {
	t.Helper()
	h := &hookReceiver{fail: map[string]int{}}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.fail[r.URL.Path] > 0 {
			h.fail[r.URL.Path]--
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		h.got = append(h.got, hookHit{path: r.URL.Path, query: r.URL.RawQuery, header: r.Header.Clone(), body: b})
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

// take returns and forgets what a path received.
func (h *hookReceiver) take(path string) []hookHit {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out, rest []hookHit
	for _, g := range h.got {
		if g.path == path {
			out = append(out, g)
		} else {
			rest = append(rest, g)
		}
	}
	h.got = rest
	return out
}

func (h hookHit) generic(t *testing.T) notify.GenericBody {
	t.Helper()
	var g notify.GenericBody
	if err := json.Unmarshal(h.body, &g); err != nil {
		t.Fatalf("not the generic body: %s", h.body)
	}
	return g
}

func (h hookHit) verifies(secret string) bool {
	ts, err := strconv.ParseInt(h.header.Get(notify.HeaderTimestamp), 10, 64)
	return err == nil && notify.Verify([]byte(secret), ts, h.body, h.header.Get(notify.HeaderSignature), time.Now(), 5*time.Minute)
}

// drain works the notifier's queue until it is empty (jobs waiting for a
// retry are made due first when due is true).
func (s *stack) drain(due bool) int {
	s.t.Helper()
	ctx := context.Background()
	n := 0
	for i := 0; i < 200; i++ {
		if due {
			if _, err := s.pool.Exec(ctx, `UPDATE jobs SET run_after = now() WHERE status = 'queued' AND kind LIKE 'notify_%'`); err != nil {
				s.t.Fatal(err)
			}
		}
		did, err := s.nw.Step(ctx)
		if err != nil {
			s.t.Fatalf("notify step: %v", err)
		}
		if !did {
			return n
		}
		n++
	}
	s.t.Fatal("the notifier's queue does not empty")
	return n
}

type createdHook struct {
	Webhook struct {
		ID      string   `json:"id"`
		URLHint string   `json:"url_hint"`
		Signed  bool     `json:"signed"`
		Events  []string `json:"events"`
	} `json:"webhook"`
	Secret string `json:"secret"`
}

func TestNotifications_EachEventReachesItsPeopleOnce(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()
	rc := newHookReceiver(t)

	alice := s.gh.AddUser(1, "alice", "Alice")
	s.res.admin = alice
	pubc := s.signInWith(alice)
	admin := s.signInWithRole(2, "dana", "write")

	// A publisher is offered the three events of their own, never the
	// administrators'.
	var settings struct {
		Events  []struct{ Kind string }
		Formats []string
		Push    struct {
			Available bool
			Events    []string
		}
	}
	pubc.get("/api/v1/me/notifications").json(t, &settings)
	if len(settings.Events) != 3 || len(settings.Formats) != 4 || settings.Push.Available {
		t.Fatalf("a publisher's settings: %+v", settings)
	}
	r := pubc.change(http.MethodPost, "/api/v1/me/webhooks", map[string]any{"name": "mine", "format": "generic",
		"url": rc.srv.URL + "/alice", "events": []string{notify.KindAppSubmitted}})
	if r.status != http.StatusBadRequest || r.code(t) != "invalid_field" {
		t.Fatalf("a publisher chose an administrators' event: %d %s", r.status, r.body)
	}
	r = pubc.change(http.MethodPost, "/api/v1/me/webhooks", map[string]any{"name": "mine", "format": "generic",
		"url": rc.srv.URL + "/alice", "events": []string{notify.KindApproved, notify.KindRejected, notify.KindFilexReleased}})
	if r.status != http.StatusCreated {
		t.Fatalf("alice's webhook: %d %s", r.status, r.body)
	}
	var aliceHook createdHook
	r.json(t, &aliceHook)
	if !strings.HasPrefix(aliceHook.Secret, "whsec_") || !aliceHook.Webhook.Signed || aliceHook.Webhook.URLHint != rc.srv.URL+"/…" {
		t.Fatalf("alice's webhook: %s", r.body)
	}
	all := []string{}
	for _, k := range notify.Kinds {
		all = append(all, k.Kind)
	}
	r = admin.change(http.MethodPost, "/api/v1/me/webhooks", map[string]any{"name": "Team alerts", "format": "generic",
		"url": rc.srv.URL + "/admin?group=filex-apps", "header_name": "Authorization", "header_value": "Bearer bn_test", "events": all})
	if r.status != http.StatusCreated {
		t.Fatalf("the administrator's webhook: %d %s", r.status, r.body)
	}
	var adminHook createdHook
	r.json(t, &adminHook)
	// Nothing secret is kept in the clear: not the address's path, the
	// signing secret or the header's value.
	var raw string
	_ = s.pool.QueryRow(ctx, `SELECT string_agg(encode(url_sealed, 'escape') || encode(COALESCE(secret_sealed, ''), 'escape') ||
		encode(COALESCE(header_sealed, ''), 'escape') || url_hint, '|') FROM notification_webhooks`).Scan(&raw)
	for _, secret := range []string{"group=filex-apps", "bn_test", adminHook.Secret, aliceHook.Secret, "/alice"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("%q is stored in the clear", secret)
		}
	}

	// --- a publisher is asked for: the administrators hear of it
	if r := pubc.change(http.MethodPost, "/api/v1/publishers", map[string]string{"id": "alice-pub", "name": "Alice", "github_owner": "alice"}); r.status != http.StatusCreated {
		t.Fatalf("request publisher: %d %s", r.status, r.body)
	}
	s.drain(false)
	hits := rc.take("/admin")
	if len(hits) != 1 || len(rc.take("/alice")) != 0 {
		t.Fatalf("publisher request: admin %d", len(hits))
	}
	h := hits[0]
	g := h.generic(t)
	if g.Event != notify.KindPublisherRequested || h.header.Get("Authorization") != "Bearer bn_test" || h.query != "group=filex-apps" ||
		!h.verifies(adminHook.Secret) || h.verifies(aliceHook.Secret) || !strings.Contains(g.Message, "alice asked for the publisher Alice (alice-pub)") ||
		g.ActionURL != s.srv.URL+"/admin/publishers" || g.Severity != "info" {
		t.Fatalf("publisher request delivery: %+v %v", g, h.header)
	}

	// --- a new app reaches review: the administrators, once
	if r := admin.change(http.MethodPatch, "/api/v1/admin/publishers/alice-pub", map[string]any{"status": "active"}); r.status != http.StatusOK {
		t.Fatalf("activate: %d %s", r.status, r.body)
	}
	submit := func(ref string) int64 {
		t.Helper()
		r := pubc.change(http.MethodPost, "/api/v1/submissions", map[string]string{"publisher": "alice-pub", "repo": "alice/filex-hello", "ref": ref})
		if r.status != http.StatusAccepted {
			t.Fatalf("submit %s: %d %s", ref, r.status, r.body)
		}
		var sub struct{ ID int64 }
		r.json(t, &sub)
		for _, k := range []string{store.JobResolve, store.JobDryRun} {
			if did, err := s.runner.Step(ctx, k); !did || err != nil {
				t.Fatalf("%s: %v %v", k, did, err)
			}
		}
		return sub.ID
	}
	first := submit("v1.0.0")
	s.drain(false)
	hits = rc.take("/admin")
	if len(hits) != 1 || hits[0].generic(t).Event != notify.KindAppSubmitted || len(rc.take("/alice")) != 0 {
		t.Fatalf("new app to review: %d", len(hits))
	}
	if g := hits[0].generic(t); g.Title != "New app to review: hello 1.0.0" || g.ActionURL != s.srv.URL+"/submissions/"+strconv.FormatInt(first, 10) {
		t.Fatalf("new app message: %+v", g)
	}
	// A reviewer's re-run of the dry runs brings it back to review: no
	// second notification.
	if r := admin.change(http.MethodPost, "/api/v1/review/submissions/"+strconv.FormatInt(first, 10)+"/recheck", map[string]any{}); r.status != http.StatusAccepted && r.status != http.StatusOK {
		t.Fatalf("recheck: %d %s", r.status, r.body)
	}
	if did, err := s.runner.Step(ctx, store.JobDryRun); !did || err != nil {
		t.Fatalf("recheck dry run: %v %v", did, err)
	}
	s.drain(false)
	if n := len(rc.take("/admin")); n != 0 {
		t.Fatalf("a recheck notified again (%d)", n)
	}

	// --- approval: the publisher's members (alice), not the reviewer; a
	// first failure (503) is retried
	rc.mu.Lock()
	rc.fail["/alice"] = 1
	rc.mu.Unlock()
	if r := admin.change(http.MethodPost, "/api/v1/review/submissions/"+strconv.FormatInt(first, 10)+"/approve", map[string]any{"note": "fine"}); r.status != http.StatusOK {
		t.Fatalf("approve: %d %s", r.status, r.body)
	}
	s.drain(false)
	if n := len(rc.take("/alice")); n != 0 {
		t.Fatalf("the 503 counted as delivered (%d)", n)
	}
	var status string
	var attempts, httpStatus int
	_ = s.pool.QueryRow(ctx, `SELECT d.status, d.attempts, d.http_status FROM notification_deliveries d
		JOIN notification_events e ON e.id = d.event_id WHERE e.kind = $1`, notify.KindApproved).Scan(&status, &attempts, &httpStatus)
	if status != "queued" || attempts != 1 || httpStatus != 503 {
		t.Fatalf("after a 503: %s, %d attempts, HTTP %d", status, attempts, httpStatus)
	}
	s.drain(true)
	hits = rc.take("/alice")
	if len(hits) != 1 || !hits[0].verifies(aliceHook.Secret) {
		t.Fatalf("approval to alice: %d", len(hits))
	}
	if g := hits[0].generic(t); g.Event != notify.KindApproved || g.Title != "Approved: hello 1.0.0" || g.Severity != "success" ||
		g.ActionURL != s.srv.URL+"/apps/hello/versions/1.0.0" || hits[0].header.Get(notify.HeaderDelivery) == "" {
		t.Fatalf("approval message: %+v", g)
	}
	if n := len(rc.take("/admin")); n != 0 {
		t.Fatalf("the reviewer heard of their own decision (%d)", n)
	}
	_ = s.pool.QueryRow(ctx, `SELECT d.status, d.attempts FROM notification_deliveries d
		JOIN notification_events e ON e.id = d.event_id WHERE e.kind = $1`, notify.KindApproved).Scan(&status, &attempts)
	if status != "delivered" || attempts != 2 {
		t.Fatalf("after the retry: %s, %d attempts", status, attempts)
	}

	// --- a new version, not approved: version_new to the administrators,
	// the reason to alice
	second := submit("v1.1.0")
	s.drain(false)
	if hits := rc.take("/admin"); len(hits) != 1 || hits[0].generic(t).Event != notify.KindVersionSubmitted {
		t.Fatalf("new version to review: %d", len(hits))
	}
	if r := admin.change(http.MethodPost, "/api/v1/review/submissions/"+strconv.FormatInt(second, 10)+"/reject",
		map[string]any{"reason": "The permissions are not explained."}); r.status != http.StatusOK {
		t.Fatalf("reject: %d %s", r.status, r.body)
	}
	s.drain(false)
	hits = rc.take("/alice")
	if len(hits) != 1 {
		t.Fatalf("rejection to alice: %d", len(hits))
	}
	if g := hits[0].generic(t); g.Event != notify.KindRejected || !strings.HasSuffix(g.Message, ": The permissions are not explained.") {
		t.Fatalf("rejection message: %+v", g)
	}

	// --- filex: the first look only remembers; a newer release is news
	// once, to everyone who chose it; the same or an older one is not
	for _, c := range []struct {
		reports []string
		want    string
	}{
		{[]string{"0.57.3", "v0.57.3 (6ca679fb, 2026-10-19T00:00:00Z)"}, ""},
		{[]string{"0.57.3", "v0.58.0 (aaaaaaaa, 2026-10-27T00:00:00Z)"}, "0.58.0"},
		{[]string{"0.57.3", "v0.58.0 (aaaaaaaa, 2026-10-27T00:00:00Z)"}, ""},
		{[]string{"0.57.3", "v0.57.9 (bbbbbbbb, x)"}, ""},
		{[]string{"v0.59.0-rc.1 (cccccccc, x)"}, ""},
	} {
		got, err := s.notify.NoteFilexVersions(ctx, c.reports)
		if err != nil || got != c.want {
			t.Fatalf("%v: %q %v, want %q", c.reports, got, err, c.want)
		}
	}
	s.drain(false)
	a, b := rc.take("/alice"), rc.take("/admin")
	if len(a) != 1 || len(b) != 1 || a[0].generic(t).Event != notify.KindFilexReleased || a[0].generic(t).Title != "filex 0.58.0 is out" {
		t.Fatalf("filex release: alice %d, admin %d", len(a), len(b))
	}

	// --- the administrator loses the role on GitHub: the next
	// administrators' event does not reach them
	s.gh.SetRole(adminRepo, "dana", "read")
	bob := s.gh.AddUser(3, "bob", "Bob")
	bobc := s.signInWith(bob)
	if r := bobc.change(http.MethodPost, "/api/v1/publishers", map[string]string{"id": "bob-pub", "name": "Bob", "github_owner": "bob"}); r.status != http.StatusCreated {
		t.Fatalf("bob's publisher: %d %s", r.status, r.body)
	}
	s.drain(false)
	if n := len(rc.take("/admin")); n != 0 {
		t.Fatalf("a former administrator heard of a publisher request (%d)", n)
	}
	// GitHub not answering: the fan-out waits rather than guess.
	s.gh.SetRole(adminRepo, "dana", "write")
	s.gh.AnswerPermission(http.StatusBadGateway, `{"message":"Server Error"}`)
	carol := s.gh.AddUser(4, "carol", "Carol")
	carolc := s.signInWith(carol)
	if r := carolc.change(http.MethodPost, "/api/v1/publishers", map[string]string{"id": "carol-pub", "name": "Carol", "github_owner": "carol"}); r.status != http.StatusCreated {
		t.Fatalf("carol's publisher: %d %s", r.status, r.body)
	}
	s.drain(false)
	var fanOuts int
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'notify_event' AND status = 'queued'`).Scan(&fanOuts)
	if fanOuts != 1 || len(rc.take("/admin")) != 0 {
		t.Fatalf("an unanswered role check: %d fan-outs waiting", fanOuts)
	}
	s.gh.AnswerPermission(0, "")
	s.drain(true)
	if hits := rc.take("/admin"); len(hits) != 1 || !strings.Contains(hits[0].generic(t).Message, "Carol (carol-pub)") {
		t.Fatalf("after GitHub answers again: %d", len(hits))
	}

	// --- a test, its outcome on the list; deleting a webhook
	r = admin.change(http.MethodPost, "/api/v1/me/webhooks/"+adminHook.Webhook.ID+"/test", map[string]any{})
	var res struct {
		Status     string `json:"status"`
		HTTPStatus int    `json:"http_status"`
	}
	r.json(t, &res)
	if r.status != http.StatusOK || res.Status != "delivered" || res.HTTPStatus != 201 {
		t.Fatalf("test: %d %s", r.status, r.body)
	}
	if hits := rc.take("/admin"); len(hits) != 1 || hits[0].generic(t).Event != notify.KindTest || !hits[0].verifies(adminHook.Secret) {
		t.Fatalf("the test delivery: %d", len(hits))
	}
	var listed struct {
		Webhooks []struct {
			ID   string
			Last *struct {
				Status     string
				HTTPStatus int `json:"http_status"`
			}
		}
	}
	admin.get("/api/v1/me/notifications").json(t, &listed)
	if len(listed.Webhooks) != 1 || listed.Webhooks[0].Last == nil || listed.Webhooks[0].Last.Status != "delivered" || listed.Webhooks[0].Last.HTTPStatus != 201 {
		t.Fatalf("the list: %+v", listed)
	}
	// Another account's webhook is not found.
	if r := pubc.change(http.MethodPost, "/api/v1/me/webhooks/"+adminHook.Webhook.ID+"/test", map[string]any{}); r.status != http.StatusNotFound {
		t.Fatalf("someone else's webhook: %d", r.status)
	}
	if r := pubc.change(http.MethodDelete, "/api/v1/me/webhooks/"+adminHook.Webhook.ID, nil); r.status != http.StatusNotFound {
		t.Fatalf("delete someone else's webhook: %d", r.status)
	}
	// Events changed, then deleted.
	r = pubc.change(http.MethodPatch, "/api/v1/me/webhooks/"+aliceHook.Webhook.ID, map[string]any{"events": []string{notify.KindFilexReleased}, "name": "releases"})
	if r.status != http.StatusOK || !strings.Contains(string(r.body), `"events":["filex.released"]`) || !strings.Contains(string(r.body), `"name":"releases"`) {
		t.Fatalf("patch: %d %s", r.status, r.body)
	}
	if r := pubc.change(http.MethodDelete, "/api/v1/me/webhooks/"+aliceHook.Webhook.ID, nil); r.status != http.StatusNoContent {
		t.Fatalf("delete: %d %s", r.status, r.body)
	}
	var actions []string
	rows, _ := s.pool.Query(ctx, `SELECT action FROM audit_log WHERE target_type = 'webhook' ORDER BY id`)
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions = append(actions, a)
	}
	rows.Close()
	if !slices.Equal(actions, []string{"webhook.create", "webhook.create", "webhook.delete"}) {
		t.Fatalf("audit: %v", actions)
	}
	// No address, secret or header value in the server's log.
	logs := s.logs.String()
	for _, secret := range []string{"bn_test", adminHook.Secret, aliceHook.Secret, "group=filex-apps"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("the log holds %q", secret)
		}
	}
}

func TestNotifications_BrowserSubscriptions(t *testing.T) {
	s := newStack(t)
	alice := s.gh.AddUser(1, "alice", "Alice")
	c := s.signInWith(alice)
	// A browser's subscription, made here: a P-256 point and a 16-byte secret.
	ua, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authBytes := make([]byte, 16)
	_, _ = rand.Read(authBytes)
	browserKeys := map[string]string{"p256dh": base64.RawURLEncoding.EncodeToString(ua.PublicKey().Bytes()),
		"auth": base64.RawURLEncoding.EncodeToString(authBytes)}
	sub := map[string]any{"endpoint": "https://push.example.net/push/abc", "expirationTime": nil, "keys": browserKeys}
	// The stack has no VAPID key: browser notifications are off.
	if r := c.change(http.MethodPost, "/api/v1/me/push-subscriptions", sub); r.status != http.StatusServiceUnavailable || r.code(t) != "push_unavailable" {
		t.Fatalf("without a VAPID key: %d %s", r.status, r.body)
	}
	pub, priv, _ := notify.GenerateVAPID()
	v, err := notify.ParseVAPID(pub, priv, "https://apps.example")
	if err != nil {
		t.Fatal(err)
	}
	s.notify.VAPID = v
	var st struct {
		Push struct {
			Available bool
			PublicKey string `json:"public_key"`
			Events    []string
		}
	}
	c.get("/api/v1/me/notifications").json(t, &st)
	if !st.Push.Available || st.Push.PublicKey != pub || len(st.Push.Events) != 3 {
		t.Fatalf("settings: %+v", st)
	}
	r := c.change(http.MethodPost, "/api/v1/me/push-subscriptions", sub)
	if r.status != http.StatusCreated {
		t.Fatalf("subscribe: %d %s", r.status, r.body)
	}
	var first struct {
		ID           string
		EndpointHost string `json:"endpoint_host"`
	}
	r.json(t, &first)
	// The same browser again: the same row.
	r = c.change(http.MethodPost, "/api/v1/me/push-subscriptions", sub)
	var again struct{ ID string }
	r.json(t, &again)
	if r.status != http.StatusOK || again.ID != first.ID || first.EndpointHost != "push.example.net" {
		t.Fatalf("again: %d %s", r.status, r.body)
	}
	for name, bad := range map[string]map[string]any{
		"http":     {"endpoint": "ftp://push.example.net/x", "keys": sub["keys"]},
		"no keys":  {"endpoint": "https://push.example.net/y", "keys": map[string]string{"p256dh": "AAAA", "auth": "AAAA"}},
		"private":  {"endpoint": "https://10.0.0.1/x", "keys": sub["keys"]},
		"no field": {"endpoint": "https://push.example.net/z"},
	} {
		if name == "private" {
			// The stack is development: a private address is taken there.
			continue
		}
		if r := c.change(http.MethodPost, "/api/v1/me/push-subscriptions", bad); r.status != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, r.status, r.body)
		}
	}
	if r := c.change(http.MethodPut, "/api/v1/me/notifications/push", map[string]any{"events": []string{notify.KindApproved}}); r.status != http.StatusOK {
		t.Fatalf("push events: %d %s", r.status, r.body)
	}
	if r := c.change(http.MethodPut, "/api/v1/me/notifications/push", map[string]any{"events": []string{notify.KindPipelineError}}); r.status != http.StatusBadRequest {
		t.Fatalf("a publisher chose an administrators' event for the browser: %d %s", r.status, r.body)
	}
	recips, err := store.NotifyRecipients(context.Background(), s.pool, notify.KindApproved, nil)
	if err != nil || len(recips) != 1 || recips[0].PushID != first.ID {
		t.Fatalf("recipients of approved: %+v %v", recips, err)
	}
	if recips, _ := store.NotifyRecipients(context.Background(), s.pool, notify.KindRejected, nil); len(recips) != 0 {
		t.Fatalf("rejected is off for the browser: %+v", recips)
	}
	if r := c.change(http.MethodDelete, "/api/v1/me/push-subscriptions/"+first.ID, nil); r.status != http.StatusNoContent {
		t.Fatalf("unsubscribe: %d %s", r.status, r.body)
	}
	if r := c.change(http.MethodDelete, "/api/v1/me/push-subscriptions/"+first.ID, nil); r.status != http.StatusNotFound {
		t.Fatalf("unsubscribe twice: %d %s", r.status, r.body)
	}
}
