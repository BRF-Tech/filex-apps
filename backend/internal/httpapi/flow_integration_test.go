//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brf-tech/filex-apps/backend/internal/auth/adminrepo"
	"github.com/brf-tech/filex-apps/backend/internal/auth/github"
	"github.com/brf-tech/filex-apps/backend/internal/auth/github/githubtest"
	"github.com/brf-tech/filex-apps/backend/internal/config"
	"github.com/brf-tech/filex-apps/backend/internal/datakey"
	"github.com/brf-tech/filex-apps/backend/internal/db/dbtest"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
	ghapi "github.com/brf-tech/filex-apps/backend/internal/github"
	"github.com/brf-tech/filex-apps/backend/internal/httpapi"
	"github.com/brf-tech/filex-apps/backend/internal/index"
	"github.com/brf-tech/filex-apps/backend/internal/install"
	"github.com/brf-tech/filex-apps/backend/internal/licensing"
	"github.com/brf-tech/filex-apps/backend/internal/media"
	"github.com/brf-tech/filex-apps/backend/internal/notify"
	"github.com/brf-tech/filex-apps/backend/internal/pipeline"
	"github.com/brf-tech/filex-apps/backend/internal/ratelimit"
	"github.com/brf-tech/filex-apps/backend/internal/resolve"
	"github.com/brf-tech/filex-apps/backend/internal/safehttp"
	"github.com/brf-tech/filex-apps/backend/internal/session"
	"github.com/brf-tech/filex-apps/backend/internal/signing"
	"github.com/brf-tech/filex-apps/backend/internal/store"
	"github.com/redis/go-redis/v9"
)

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func pngOf(w, h int) []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)))
	return b.Bytes()
}

// fakeResolver plays the validator lane's resolver: it reads nothing,
// stores the icon through the real media sink and resolves every tag
// "vX" of alice/filex-hello to version X.
type fakeResolver struct {
	sink pipeline.MediaSink
	wasm string
	icon []byte
	// admin is the token GitHub says administers alice/filex-hello.
	admin string
}

// RepoAccess plays check 1 with the submitter's token.
func (f *fakeResolver) RepoAccess(ctx context.Context, repo, userToken string) (*ghapi.Repo, domain.CheckResult, error) {
	now := time.Now()
	if !strings.EqualFold(repo, "alice/filex-hello") || userToken == "" || userToken != f.admin {
		return nil, domain.CheckResult{Kind: domain.CheckRepoAccess, Status: domain.CheckFailed, Code: "repo_permission",
			Message: "listing " + repo + " needs the admin or maintain role on it", StartedAt: now, EndedAt: &now}, nil
	}
	data, _ := json.Marshal(map[string]any{"repo_id": 9001, "owner_id": 1, "permission": "admin"})
	return &ghapi.Repo{ID: 9001, Name: "filex-hello", FullName: "alice/filex-hello", Owner: ghapi.Owner{ID: 1, Login: "alice", Type: "User"},
			Permissions: &ghapi.Permissions{Admin: true}},
		domain.CheckResult{Kind: domain.CheckRepoAccess, Status: domain.CheckPassed, Data: data, StartedAt: now, EndedAt: &now}, nil
}

func (f *fakeResolver) Resolve(ctx context.Context, req domain.ResolveRequest) (*domain.Resolution, []domain.CheckResult, error) {
	now := time.Now()
	version := strings.TrimPrefix(req.Ref, "v")
	// An interface app that opens and makes .hello files (what the fake dry
	// run's permission rows and file_types say too).
	manifest := json.RawMessage(`{"name":"hello","version":"` + version + `","label":{"en":"Hello","tr":"Merhaba"},"permissions":["files:read"],"engines":{"ffmpeg":true},` +
		`"views":[{"id":"viewer","placement":"viewer","label":{"en":"Hello"},"ui":"index.html","applies":{"ext":["hello"]}}],` +
		`"new_documents":[{"ext":"hello","label":{"en":"Hello file"},"view":"viewer"}]}`)
	ref := domain.MediaRef{Role: "icon", SHA256: sha(f.icon), Type: "image/png", Width: 128, Height: 128}
	if err := f.sink.Put(ctx, ref, "https://raw.githubusercontent.com/alice/filex-hello/icon.png", f.icon); err != nil {
		return nil, nil, err
	}
	res := &domain.Resolution{
		RepoOwner: "alice", RepoName: "filex-hello", RepoID: 9001, OwnerID: 1, OwnerType: "User", Ref: req.Ref,
		CommitSHA: strings.Repeat("c", 40), ManifestURL: "https://raw.githubusercontent.com/alice/filex-hello/" + strings.Repeat("c", 40) + "/filex-app.json",
		ManifestSHA256: sha(manifest), Manifest: manifest, Name: "hello", Version: version, Kind: domain.KindApp,
		FilexRange: ">=0.49.0", Permissions: []string{"files:read"}, Languages: []string{"en", "tr"}, License: "MIT",
		Wasm:        &domain.Artifact{URL: "https://github.com/alice/filex-hello/releases/download/" + req.Ref + "/hello.wasm", SHA256: f.wasm, Size: 1234},
		UI:          &domain.Artifact{URL: "https://github.com/alice/filex-hello/releases/download/" + req.Ref + "/ui.zip", SHA256: strings.Repeat("e", 64), Size: 4321},
		Consent:     domain.Consent{Publisher: req.Publisher, Categories: []string{"documents", "no-such-category"}, Summary: domain.Text{"en": "Says hello"}},
		Description: "# Hello\n\nIt greets. <script>alert(1)</script>",
		Media:       []domain.MediaRef{ref},
	}
	var checks []domain.CheckResult
	for _, k := range []domain.CheckKind{domain.CheckTag, domain.CheckManifest, domain.CheckConsent, domain.CheckArtifacts, domain.CheckMedia} {
		checks = append(checks, domain.CheckResult{Kind: k, Status: domain.CheckPassed, StartedAt: now, EndedAt: &now})
	}
	return res, checks, nil
}

// fakeDryRunner plays both validators: latest answers, floor skips. Like a
// real filex 0.50.0 for an app with an interface, the answer lists the
// permissions filex derives from the manifest's `ui` block (ui,
// ui-viewer:.ext, ui-new:.ext) next to the declared files:read
// (measured by the e2e full flow, 2026-10-04).
type fakeDryRunner struct{}

func (fakeDryRunner) DryRun(ctx context.Context, req domain.DryRunRequest) ([]domain.DryRunResult, []domain.CheckResult, error) {
	now := time.Now()
	ok := true
	latest := domain.DryRunResult{Validator: "latest", FilexVersion: "0.50.0", OK: true, Kind: req.Kind, CompatOK: &ok,
		WasmSHA256: req.ExpectWasmSHA256, UISHA256: req.ExpectUISHA256,
		Permissions: []domain.PermissionRow{{ID: "files:read", Label: "Reads the files it is opened on"},
			{ID: "ui", Label: "Shows its own interface"}, {ID: "ui-viewer:.hello", Label: "Opens .hello files"},
			{ID: "ui-new:.hello", Label: "Creates .hello files"}},
		Languages: []domain.LanguageCoverage{{Code: "en", Percent: 100, Keys: 10, Total: 10}, {Code: "tr", Percent: 90, Keys: 9, Total: 10}},
		Raw:       json.RawMessage(`{"kind":"app","file_types":[{"ext":"hello"}]}`)}
	return []domain.DryRunResult{latest}, []domain.CheckResult{
		{Kind: domain.CheckDryRun, Validator: "latest", Status: domain.CheckPassed, StartedAt: now, EndedAt: &now},
		{Kind: domain.CheckDryRun, Validator: "floor", Status: domain.CheckSkipped, Code: "out_of_range", StartedAt: now, EndedAt: &now},
	}, nil
}

type stack struct {
	t      *testing.T
	srv    *httptest.Server
	pool   *pgxpool.Pool
	gh     *githubtest.Server
	roles  *adminrepo.Checker
	runner *pipeline.Runner
	pub    *index.Releaser
	keys   *index.Keyring
	res    *fakeResolver
	// logs is everything the server logged (the license tests read it to
	// prove no key or address reaches a log).
	logs *lockedBuffer
	lic  *licensing.Service
	inst *install.Service
	// notify and nw are the notifications and their worker (a test steps it).
	notify *notify.Service
	nw     *notify.Worker
}

// lockedBuffer is a log sink the server's goroutines share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// testDataKey is the stacks' FAPPS_DATA_KEY.
var testDataKey = bytes.Repeat([]byte{9}, 32)

// adminRepo is the stacks' administration repository (private, in the fake
// GitHub): write on it makes an administrator, triage a reviewer.
const adminRepo = "acme/store-admins"

// newStack is the store with a server token that sees the administration
// repository.
func newStack(t *testing.T) *stack { return newStackWith(t, true) }

// newStackWith: seesAdminRepo=false is a fine-grained token the
// organization has not approved yet (it sees nothing).
func newStackWith(t *testing.T, seesAdminRepo bool) *stack {
	return newStackOpts(t, stackOpts{seesAdminRepo: seesAdminRepo})
}

type stackOpts struct {
	seesAdminRepo bool
	noDataKey     bool // FAPPS_DATA_KEY not set
	// noAdminRepo: FAPPS_ADMIN_REPO not set (the lists alone decide);
	// admins and reviewers are FAPPS_ADMINS and FAPPS_REVIEWERS.
	noAdminRepo       bool
	admins, reviewers []config.Member
}

func newStackOpts(t *testing.T, o stackOpts) *stack {
	t.Helper()
	seesAdminRepo := o.seesAdminRepo
	ctx := context.Background()
	logs := &lockedBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	pool := dbtest.Migrated(t)
	gh := githubtest.New()
	t.Cleanup(gh.Close)
	gh.AddRepo(adminRepo, 1404478182)
	serverToken := gh.ServerToken()
	if seesAdminRepo {
		serverToken = gh.ServerToken(adminRepo)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	public := "http://" + ln.Addr().String()
	cfg := &config.Config{PublicURL: public, Env: "development", Index: config.IndexConfig{TTL: 60 * 24 * time.Hour, RefreshBefore: 30 * 24 * time.Hour},
		Validators: []config.Validator{{Label: "latest", BaseURL: "http://filex-latest:5212"}, {Label: "floor", BaseURL: "http://filex-floor:5212"}}}

	keys, err := index.LoadKeys(ctx, pool, config.KeyConfig{DemoKeys: true}, "development", log)
	if err != nil || keys.Index == nil || keys.Artifact == nil {
		t.Fatalf("demo keys: %v", err)
	}
	sessions, err := session.NewManager(pool, bytes.Repeat([]byte{3}, 32), false)
	if err != nil {
		t.Fatal(err)
	}
	redisURL := os.Getenv("FAPPS_TEST_REDIS_URL")
	if redisURL == "" {
		t.Fatal("FAPPS_TEST_REDIS_URL is not set")
	}
	opt, _ := redis.ParseURL(redisURL)
	rc := redis.NewClient(opt)
	t.Cleanup(func() { _ = rc.Close() })
	if err := rc.FlushDB(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	limiter := &ratelimit.Fallback{Primary: &ratelimit.Redis{Client: rc, Prefix: "fapps:rl:"}, Secondary: ratelimit.NewMemory()}

	ghp := github.New(gh.ClientID, gh.ClientSecret, gh.URL, gh.URL, public+"/auth/github/callback")
	repo := adminRepo
	if o.noAdminRepo {
		repo = ""
	}
	roles := adminrepo.Open(adminrepo.Options{GitHub: ghp, Repo: repo, Token: serverToken, Admins: o.admins, Reviewers: o.reviewers, Log: log})
	pub := &index.Releaser{Pool: pool, Keys: keys, PublicURL: public, TTL: cfg.Index.TTL, RefreshBefore: cfg.Index.RefreshBefore, Log: log}
	sink := &media.Sink{DB: pool, MaxBytes: 5 << 20}
	res := &fakeResolver{sink: sink, wasm: strings.Repeat("d", 64), icon: pngOf(128, 128)}
	runner := &pipeline.Runner{Pool: pool, Resolver: res, DryRunner: fakeDryRunner{}, Index: pub, Log: log, Validators: 2,
		Permissions: pipeline.PermissionRule{Normalize: resolve.NormalizePermissions, IsDerived: resolve.IsDerivedPermission}}
	svc := &pipeline.Service{Pool: pool, Repos: res, Keys: keys, Index: pub, Kick: runner.Kick, Log: log}

	var data *datakey.Keys
	if !o.noDataKey {
		if data, err = datakey.New(testDataKey); err != nil {
			t.Fatal(err)
		}
	}
	lic := &licensing.Service{Pool: pool, Keys: keys, Data: data, Log: log}
	ins := &install.Service{Pool: pool, Keys: keys, Data: data, StoreOrigin: public, Development: true, Log: log}
	// Notifications: webhooks on this machine (development: plain http and
	// loopback), the role check of the administrators' events on the fake
	// GitHub, asked every time (no cache).
	hooks, err := safehttp.Open(safehttp.Config{Env: "development", AnyHost: true, AllowHTTP: true, AllowPrivate: true, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ntf := &notify.Service{Pool: pool, Data: data, Sender: &notify.Sender{Client: hooks, UserAgent: "fapps-test"},
		PublicURL: public, Development: true, Log: log}
	nw := &notify.Worker{Svc: ntf, Admins: &notify.RepoAdmins{Pool: pool, Roles: roles, TTL: time.Nanosecond}, Log: log}

	h := httpapi.New(httpapi.Deps{Config: cfg, Pool: pool, Sessions: sessions, GitHub: ghp, Roles: roles, Limiter: limiter,
		Index: pub, Service: svc, Licensing: lic, Install: ins, Notify: ntf, Log: log})
	srv := httptest.NewUnstartedServer(h)
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return &stack{t: t, srv: srv, pool: pool, gh: gh, roles: roles, runner: runner, pub: pub, keys: keys, res: res,
		logs: logs, lic: lic, inst: ins, notify: ntf, nw: nw}
}

// client is a browser: a cookie jar, redirects followed, the CSRF token
// and the store's Origin on every change.
type client struct {
	s    *stack
	http *http.Client
	csrf string
	xff  string
}

func (s *stack) newClient() *client {
	jar, _ := cookiejar.New(nil)
	return &client{s: s, http: &http.Client{Jar: jar, Timeout: 30 * time.Second}}
}

type resp struct {
	status int
	header http.Header
	body   []byte
}

func (r resp) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("not JSON (%d): %s", r.status, r.body)
	}
}

func (r resp) code(t *testing.T) string {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(r.body, &e)
	return e.Error
}

func (c *client) do(method, path string, body any, hdr map[string]string) resp {
	c.s.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.s.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.xff != "" {
		req.Header.Set("X-Forwarded-For", c.xff)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.s.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, header: res.Header, body: b}
}

func (c *client) get(path string) resp { return c.do(http.MethodGet, path, nil, nil) }

// change sends a state-changing request the way the web interface does.
func (c *client) change(method, path string, body any) resp {
	return c.do(method, path, body, map[string]string{"Origin": c.s.srv.URL, "X-CSRF-Token": c.csrf})
}

func (c *client) signIn(path string) {
	c.s.t.Helper()
	r := c.get(path + "?return_to=/after")
	if r.status != http.StatusServiceUnavailable && r.status != http.StatusOK {
		// The redirect chain ends at /after (the SPA, not built in tests: 503).
		c.s.t.Fatalf("sign-in ended with %d: %s", r.status, r.body)
	}
	me := c.get("/api/v1/me")
	if me.status != http.StatusOK {
		c.s.t.Fatalf("not signed in after %s: %d %s", path, me.status, me.body)
	}
	var m struct {
		CSRF string `json:"csrf_token"`
	}
	me.json(c.s.t, &m)
	c.csrf = m.CSRF
}

func TestFullFlow(t *testing.T) {
	s := newStack(t)
	ctx := context.Background()

	// The first index: empty, signed, serial 1.
	if r, err := s.pub.EnsureFresh(ctx); err != nil || !r.Published || r.Release.Serial != 1 || r.Release.Reason != index.ReasonInitial {
		t.Fatalf("initial index: %+v %v", r, err)
	}

	// --- publisher signs in with GitHub
	alice := s.gh.AddUser(1, "alice", "Alice")
	s.res.admin = alice
	s.gh.SignInAs(alice)
	pubc := s.newClient()
	pubc.signIn("/auth/github/login")
	var me struct {
		Identity   struct{ Provider, Login string }
		Roles      []string
		RoleSource string          `json:"role_source"`
		Providers  map[string]bool `json:"providers"`
	}
	pubc.get("/api/v1/me").json(t, &me)
	if me.Identity.Provider != "github" || me.Identity.Login != "alice" || len(me.Roles) != 0 || !me.Providers["github"] ||
		len(me.Providers) != 1 || me.RoleSource != "github repo permission "+adminRepo+": none" {
		t.Fatalf("me = %+v", me)
	}

	// --- CSRF: a missing token, a wrong one and a foreign origin are refused
	body := map[string]string{"id": "alice-pub", "name": "Alice", "github_owner": "alice"}
	for name, hdr := range map[string]map[string]string{
		"missing": {"Origin": s.srv.URL},
		"wrong":   {"Origin": s.srv.URL, "X-CSRF-Token": pubc.csrf + "x"},
		"origin":  {"Origin": "https://evil.example", "X-CSRF-Token": pubc.csrf},
	} {
		if r := pubc.do(http.MethodPost, "/api/v1/publishers", body, hdr); r.status != http.StatusForbidden || r.code(t) != "csrf_failed" {
			t.Fatalf("CSRF %s: %d %s", name, r.status, r.body)
		}
	}
	var n int
	_ = s.pool.QueryRow(ctx, "SELECT count(*) FROM publishers").Scan(&n)
	if n != 0 {
		t.Fatal("a refused request wrote a publisher")
	}

	r := pubc.change(http.MethodPost, "/api/v1/publishers", body)
	if r.status != http.StatusCreated || !strings.Contains(string(r.body), `"status":"pending"`) {
		t.Fatalf("request publisher: %d %s", r.status, r.body)
	}
	if r := pubc.change(http.MethodPost, "/api/v1/publishers", body); r.status != http.StatusConflict || r.code(t) != "publisher_exists" {
		t.Fatalf("second request: %d %s", r.status, r.body)
	}
	sub := map[string]string{"publisher": "alice-pub", "repo": "https://github.com/alice/filex-hello", "ref": "v1.0.0"}
	if r := pubc.change(http.MethodPost, "/api/v1/submissions", sub); r.status != http.StatusForbidden || r.code(t) != "publisher_not_active" {
		t.Fatalf("submit while pending: %d %s", r.status, r.body)
	}

	// --- an administrator (write on the administration repository) signs in
	// with GitHub, like everyone, and activates the publisher
	admin := s.signInWithRole(2, "dana", "write")
	if r := pubc.change(http.MethodPatch, "/api/v1/admin/publishers/alice-pub", map[string]any{"status": "active"}); r.status != http.StatusForbidden || r.code(t) != "forbidden" {
		t.Fatalf("a publisher on an admin route: %d %s", r.status, r.body)
	}
	if r := admin.change(http.MethodPatch, "/api/v1/admin/publishers/alice-pub", map[string]any{"status": "active", "verified": true}); r.status != http.StatusOK {
		t.Fatalf("activate: %d %s", r.status, r.body)
	}

	// --- submit; the same repository at the same tag again is refused
	r = pubc.change(http.MethodPost, "/api/v1/submissions", sub)
	if r.status != http.StatusAccepted {
		t.Fatalf("submit: %d %s", r.status, r.body)
	}
	var submitted struct {
		ID     int64
		Status string
		Repo   string
	}
	r.json(t, &submitted)
	if submitted.Status != "queued" || submitted.Repo != "alice/filex-hello" {
		t.Fatalf("submission = %+v", submitted)
	}
	if r := pubc.change(http.MethodPost, "/api/v1/submissions", sub); r.status != http.StatusConflict || r.code(t) != "submission_open" ||
		!strings.Contains(string(r.body), `"id":`+strconv.FormatInt(submitted.ID, 10)) {
		t.Fatalf("second submit: %d %s", r.status, r.body)
	}
	other := map[string]string{"publisher": "alice-pub", "repo": "alice/someone-elses", "ref": "v1.0.0"}
	if r := pubc.change(http.MethodPost, "/api/v1/submissions", other); r.status != http.StatusForbidden || r.code(t) != "repo_permission" {
		t.Fatalf("a repository GitHub says alice may not list: %d %s", r.status, r.body)
	}

	// --- the pipeline: resolve, then the dry runs
	if did, err := s.runner.Step(ctx, store.JobResolve); !did || err != nil {
		t.Fatalf("resolve step: %v %v", did, err)
	}
	if did, err := s.runner.Step(ctx, store.JobDryRun); !did || err != nil {
		t.Fatalf("dry-run step: %v %v", did, err)
	}
	var detail struct {
		Status  string
		Checks  []struct{ Kind, Status, Validator, Code string }
		DryRuns []struct {
			Validator   string
			Permissions []struct{ ID, Label string }
		} `json:"dry_runs"`
	}
	path := "/api/v1/submissions/" + strconv.FormatInt(submitted.ID, 10)
	pubc.get(path).json(t, &detail)
	if detail.Status != "needs_review" {
		t.Fatalf("after the pipeline: %+v", detail)
	}
	kinds := map[string]string{}
	for _, c := range detail.Checks {
		kinds[c.Kind+"/"+c.Validator] = c.Status
	}
	for k, want := range map[string]string{"repo_access/": "passed", "tag/": "passed", "policy/": "passed", "dry_run/latest": "passed", "dry_run/floor": "skipped"} {
		if kinds[k] != want {
			t.Fatalf("check %s = %q, want %s (all: %v)", k, kinds[k], want, kinds)
		}
	}
	if len(detail.DryRuns) != 1 || detail.DryRuns[0].Permissions[0].Label != "Reads the files it is opened on" {
		t.Fatalf("dry runs = %+v", detail.DryRuns)
	}
	var queue struct{ Total int }
	admin.get("/api/v1/review/queue").json(t, &queue)
	if queue.Total != 1 {
		t.Fatalf("review queue total = %d", queue.Total)
	}

	// --- approve: version, signatures, serial + 1
	before := latestSerial(t, s)
	r = admin.change(http.MethodPost, "/api/v1/review/submissions/"+strconv.FormatInt(submitted.ID, 10)+"/approve", map[string]any{"note": "fine"})
	if r.status != http.StatusOK {
		t.Fatalf("approve: %d %s", r.status, r.body)
	}
	var approved struct {
		IndexSerial int64 `json:"index_serial"`
		Version     struct {
			Version string
			Wasm    struct{ SHA256, Sig string }
			Install struct {
				GitHubRepo string `json:"github_repo"`
				Ref        string
			}
		}
	}
	r.json(t, &approved)
	if approved.IndexSerial != before+1 || latestSerial(t, s) != before+1 {
		t.Fatalf("approve published serial %d (latest %d), want %d", approved.IndexSerial, latestSerial(t, s), before+1)
	}
	if approved.Version.Install.GitHubRepo != "alice/filex-hello" || approved.Version.Install.Ref != "v1.0.0" {
		t.Fatalf("install = %+v", approved.Version.Install)
	}
	if r := admin.change(http.MethodPost, "/api/v1/review/submissions/"+strconv.FormatInt(submitted.ID, 10)+"/approve", map[string]any{}); r.status != http.StatusConflict || r.code(t) != "already_decided" {
		t.Fatalf("approve twice: %d %s", r.status, r.body)
	}

	doc := s.verifiedIndex(approved.IndexSerial)
	if len(doc.Apps) != 1 || doc.Apps[0].Name != "hello" || doc.Apps[0].Latest != "1.0.0" || doc.Apps[0].Versions[0].Yanked != nil {
		t.Fatalf("index after approve: %+v", doc.Apps)
	}
	app := doc.Apps[0]
	if strings.Join(app.Categories, ",") != "documents" || app.Label["tr"] != "Merhaba" || app.Icon == nil || app.License != "MIT" {
		t.Fatalf("listing = %+v", app)
	}
	v := app.Versions[0]
	art := artifactKey(t, doc)
	if v.Wasm == nil || signing.Verify(art, v.Wasm.SHA256, v.Wasm.Sig) != nil || signing.Verify(art, v.Manifest.SHA256, v.Manifest.Sig) != nil {
		t.Fatalf("artifact signatures do not verify: %+v", v)
	}
	if len(v.Languages) != 2 || v.Languages[1].Code != "tr" || v.Languages[1].Percent != 90 || strings.Join(v.Engines, ",") != "ffmpeg" {
		t.Fatalf("version = %+v", v)
	}

	// The icon is mirrored, never hot-linked, and served inert.
	icon := s.newClient().get(strings.TrimPrefix(app.Icon.URL, s.srv.URL))
	if icon.status != http.StatusOK || icon.header.Get("Content-Type") != "image/png" || sha(icon.body) != app.Icon.SHA256 ||
		!strings.Contains(icon.header.Get("Content-Security-Policy"), "default-src 'none'") || !strings.Contains(icon.header.Get("Cache-Control"), "immutable") {
		t.Fatalf("icon: %d %v", icon.status, icon.header)
	}

	// The catalog shows it; its description is sanitized.
	anon := s.newClient()
	var cat struct {
		Total int
		Items []struct{ Name, Latest, Compatible string }
	}
	anon.get("/api/v1/apps?q=hello&filex=0.50.0").json(t, &cat)
	if cat.Total != 1 || cat.Items[0].Latest != "1.0.0" || cat.Items[0].Compatible != "1.0.0" {
		t.Fatalf("catalog = %+v", cat)
	}
	anon.get("/api/v1/apps?filex=0.48.0").json(t, &cat)
	if cat.Total != 0 {
		t.Fatalf("filex 0.48.0 is outside >=0.49.0: %+v", cat)
	}
	page := anon.get("/api/v1/apps/hello")
	if page.status != http.StatusOK || strings.Contains(string(page.body), "<script") || !strings.Contains(string(page.body), "<h1>Hello</h1>") {
		t.Fatalf("app page: %d %s", page.status, page.body)
	}
	vd := anon.get("/api/v1/apps/hello/versions/1.0.0")
	if vd.status != http.StatusOK || !strings.Contains(string(vd.body), "Reads the files it is opened on") {
		t.Fatalf("version page: %d %s", vd.status, vd.body)
	}
	// The approval recorded the kinds of file it works with (0007): the
	// version's page lists them and the file-type search finds it.
	if !strings.Contains(string(vd.body), `"file_types":[{"ext":"hello","roles":[{"role":"open","id":"viewer","label":{"en":"Hello"}},{"role":"create","id":"viewer","label":{"en":"Hello file"}}]}]`) {
		t.Fatalf("version page file_types: %s", vd.body)
	}
	if names := typeSearch(t, anon, ".hello"); strings.Join(names, ",") != "hello:open(viewer)+create(viewer)" {
		t.Fatalf("a search for .hello after the approval: %v", names)
	}

	// --- yank: a new index with `yanked`, and `latest` skips it
	yankPath := "/api/v1/admin/apps/hello/versions/1.0.0/yank"
	if r := admin.do(http.MethodPost, yankPath, map[string]string{"reason": "crashes"}, map[string]string{"Origin": s.srv.URL}); r.status != http.StatusForbidden || r.code(t) != "csrf_failed" {
		t.Fatalf("yank without the token: %d %s", r.status, r.body)
	}
	if r := admin.change(http.MethodPost, yankPath, map[string]string{"reason": "crashes"}); r.status != http.StatusOK || !strings.Contains(string(r.body), `"reason":"crashes"`) {
		t.Fatalf("yank: %d %s", r.status, r.body)
	}
	doc = s.verifiedIndex(approved.IndexSerial + 1)
	if y := doc.Apps[0].Versions[0].Yanked; y == nil || y.Reason != "crashes" || doc.Apps[0].Latest != "" {
		t.Fatalf("index after yank: %+v", doc.Apps[0])
	}
	// Only the newest version that is not yanked speaks for the app.
	if names := typeSearch(t, anon, ".hello"); len(names) != 0 {
		t.Fatalf("a yanked version is still found by its kinds: %v", names)
	}
	if r := admin.change(http.MethodDelete, yankPath, nil); r.status != http.StatusOK {
		t.Fatalf("unyank: %d %s", r.status, r.body)
	}
	doc = s.verifiedIndex(approved.IndexSerial + 2)
	if doc.Apps[0].Versions[0].Yanked != nil || doc.Apps[0].Latest != "1.0.0" {
		t.Fatalf("index after unyank: %+v", doc.Apps[0])
	}
	if names := typeSearch(t, anon, "hello"); strings.Join(names, ",") != "hello:open(viewer)+create(viewer)" {
		t.Fatalf("after the unyank, a bare known extension: %v", names)
	}

	// --- revoke shows in the index and hides the app from the lists
	if r := admin.change(http.MethodPost, "/api/v1/admin/apps/hello/revoke", map[string]string{"reason": "harmful"}); r.status != http.StatusOK {
		t.Fatalf("revoke: %d %s", r.status, r.body)
	}
	doc = s.verifiedIndex(approved.IndexSerial + 3)
	if doc.Apps[0].Revoked == nil || doc.Apps[0].Revoked.Reason != "harmful" {
		t.Fatalf("index after revoke: %+v", doc.Apps[0])
	}
	anon.get("/api/v1/apps").json(t, &cat)
	if cat.Total != 0 {
		t.Fatal("a revoked app is still listed")
	}

	// --- the audit log verifies and holds the decisions
	var audit struct {
		ChainOK bool `json:"chain_ok"`
		Total   int
		Items   []struct {
			Action    string `json:"action"`
			ActorRole string `json:"actor_role"`
		}
	}
	admin.get("/api/v1/admin/audit?per_page=100").json(t, &audit)
	if !audit.ChainOK {
		t.Fatal("audit chain does not verify")
	}
	seen := map[string]bool{}
	for _, it := range audit.Items {
		seen[it.Action] = true
	}
	for _, a := range []string{"auth.signin", "publisher.request", "publisher.update", "submission.create", "submission.approve", "version.yank", "version.unyank", "app.revoke"} {
		if !seen[a] {
			t.Errorf("audit lacks %s (has %v)", a, seen)
		}
	}

	// --- logout ends the session
	if r := pubc.change(http.MethodPost, "/auth/logout", nil); r.status != http.StatusNoContent {
		t.Fatalf("logout: %d %s", r.status, r.body)
	}
	if r := pubc.get("/api/v1/me"); r.status != http.StatusUnauthorized {
		t.Fatalf("after logout: %d", r.status)
	}
}

func TestSignInStartsAreRateLimited(t *testing.T) {
	s := newStack(t)
	c := s.newClient()
	c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.xff = "198.51.100.77"
	// The interface learns which sign-ins exist from /api/v1/me (signed
	// out: 401 with `providers`); asking does not spend the sign-in budget.
	for i := 0; i < 3*ratelimit.SignIn.Limit; i++ {
		r := c.get("/api/v1/me")
		var body struct {
			Error     string          `json:"error"`
			Providers map[string]bool `json:"providers"`
		}
		r.json(t, &body)
		if r.status != http.StatusUnauthorized || body.Error != "unauthorized" || !body.Providers["github"] || len(body.Providers) != 1 {
			t.Fatalf("signed-out /api/v1/me %d: %d %s, want 401 with providers github only", i+1, r.status, r.body)
		}
	}
	for i := 1; i <= ratelimit.SignIn.Limit; i++ {
		if r := c.get("/auth/github/login"); r.status != http.StatusFound {
			t.Fatalf("sign-in start %d: %d %s", i, r.status, r.body)
		}
	}
	r := c.get("/auth/github/login")
	if r.status != http.StatusTooManyRequests || r.code(t) != "rate_limited" {
		t.Fatalf("11th sign-in start a minute: %d %s", r.status, r.body)
	}
	ra, _ := strconv.Atoi(r.header.Get("Retry-After"))
	if ra < 1 || ra > 60 {
		t.Fatalf("Retry-After = %q", r.header.Get("Retry-After"))
	}
	other := s.newClient()
	other.http.CheckRedirect = c.http.CheckRedirect
	other.xff = "198.51.100.78"
	if r := other.get("/auth/github/login"); r.status == http.StatusTooManyRequests {
		t.Fatal("another address shares the count")
	}
}

// signInWithRole makes a GitHub account with role on the administration
// repository ("" = not a collaborator) and signs a new browser in with it.
func (s *stack) signInWithRole(id int64, login, role string) *client {
	s.t.Helper()
	return s.signInWith(s.account(id, login, role))
}

// account makes a GitHub account with role on the administration
// repository and returns its token.
func (s *stack) account(id int64, login, role string) string {
	token := s.gh.AddUser(id, login, login)
	if role != "" {
		s.gh.SetRole(adminRepo, login, role)
	}
	return token
}

// signInWith signs a new browser in with the account of token.
func (s *stack) signInWith(token string) *client {
	s.t.Helper()
	s.gh.SignInAs(token)
	c := s.newClient()
	c.signIn("/auth/github/login")
	return c
}

func latestSerial(t *testing.T, s *stack) int64 {
	t.Helper()
	n, err := store.LatestSerial(context.Background(), s.pool)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// verifiedIndex fetches /v1/index.json and its signature the way a filex
// would, checks serial, canonical bytes, ETag, signature and expiry, and
// returns the document.
func (s *stack) verifiedIndex(wantSerial int64) index.Document {
	t := s.t
	t.Helper()
	c := s.newClient()
	r := c.get("/v1/index.json")
	sig := c.get("/v1/index.json.sig")
	if r.status != http.StatusOK || sig.status != http.StatusOK {
		t.Fatalf("index: %d, sig: %d", r.status, sig.status)
	}
	if r.header.Get("X-Index-Serial") != strconv.FormatInt(wantSerial, 10) || sig.header.Get("X-Index-Serial") != r.header.Get("X-Index-Serial") {
		t.Fatalf("serial = %s / %s, want %d", r.header.Get("X-Index-Serial"), sig.header.Get("X-Index-Serial"), wantSerial)
	}
	var doc index.Document
	if err := json.Unmarshal(r.body, &doc); err != nil {
		t.Fatal(err)
	}
	canon, _ := index.Canonical(doc)
	if !bytes.Equal(canon, r.body) {
		t.Fatalf("served bytes are not canonical:\n%s", r.body)
	}
	if r.header.Get("ETag") != `"`+sha(r.body)+`"` || r.header.Get("Cache-Control") != "public, max-age=300" {
		t.Fatalf("headers: %v", r.header)
	}
	if nm := c.do(http.MethodGet, "/v1/index.json", nil, map[string]string{"If-None-Match": r.header.Get("ETag")}); nm.status != http.StatusNotModified {
		t.Fatalf("If-None-Match: %d", nm.status)
	}
	var idxKeys []ed25519.PublicKey
	for _, k := range doc.Keys {
		if k.Use == "index" && k.Status == "active" {
			pk, _ := signing.ParsePublicKey(k.Ed25519)
			idxKeys = append(idxKeys, pk)
		}
	}
	if len(idxKeys) != 1 || hex.EncodeToString(idxKeys[0]) != s.keys.Index.PublicHex() {
		t.Fatalf("index keys = %+v", doc.Keys)
	}
	if err := signing.Verify(idxKeys, sha(r.body), strings.TrimSpace(string(sig.body))); err != nil {
		t.Fatalf("index signature: %v", err)
	}
	exp, _ := time.Parse(time.RFC3339, doc.ExpiresAt)
	if doc.Serial != wantSerial || time.Until(exp) < 59*24*time.Hour {
		t.Fatalf("serial %d, expires %s", doc.Serial, doc.ExpiresAt)
	}
	return doc
}

func artifactKey(t *testing.T, doc index.Document) []ed25519.PublicKey {
	t.Helper()
	for _, k := range doc.Keys {
		if k.Use == "artifact" && k.Status == "active" {
			pk, err := signing.ParsePublicKey(k.Ed25519)
			if err != nil {
				t.Fatal(err)
			}
			return []ed25519.PublicKey{pk}
		}
	}
	t.Fatal("no artifact key in the index")
	return nil
}
