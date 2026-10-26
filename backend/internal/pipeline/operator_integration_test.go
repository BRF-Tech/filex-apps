//go:build integration

package pipeline_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brf-tech/filex-apps/backend/internal/audit"
	"github.com/brf-tech/filex-apps/backend/internal/config"
	"github.com/brf-tech/filex-apps/backend/internal/db/dbtest"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
	ghapi "github.com/brf-tech/filex-apps/backend/internal/github"
	"github.com/brf-tech/filex-apps/backend/internal/index"
	"github.com/brf-tech/filex-apps/backend/internal/media"
	"github.com/brf-tech/filex-apps/backend/internal/pipeline"
	"github.com/brf-tech/filex-apps/backend/internal/resolve"
	"github.com/brf-tech/filex-apps/backend/internal/safehttp"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// The operator import end to end against a real PostgreSQL: the real
// resolver (reading a GitHub stand-in over TLS), the real media sink, the
// real job queue, approval and the signed index. Only the validators are a
// stand-in (their real dry runs are the validator lane's integration tests
// and the live import).

const (
	brfTechID   = 159972728
	demoRepo    = "BRF-Tech/filex-demo"
	demoRepoID  = 7001
	otherRepo   = "someone/else"
	demoCommit  = "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00"
	otherCommit = "0badc0de0badc0de0badc0de0badc0de0badc0de"
)

var demoWasm = []byte("\x00asm\x01\x00\x00\x00demo")

func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func pngImage(w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(i % 251)
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// gitHub plays api.github.com, raw.githubusercontent.com and the release
// downloads for two repositories: BRF-Tech/filex-demo (the organization
// BRF-Tech, no .filex-apps.json at its tag) and someone/else.
func gitHub(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		repoJSON := func(full string, id, ownerID int64, ownerType string) {
			owner, name, _ := strings.Cut(full, "/")
			m := map[string]any{"id": id, "name": name, "full_name": full, "private": false, "archived": false, "visibility": "public",
				"owner": map[string]any{"id": ownerID, "login": owner, "type": ownerType}}
			if r.Header.Get("Authorization") == "Bearer user-token" {
				m["permissions"] = map[string]bool{"admin": true, "maintain": true, "push": true, "pull": true}
			}
			_ = json.NewEncoder(w).Encode(m)
		}
		manifest := func(full, name string) []byte {
			return []byte(`{"manifest_version":1,"name":"` + name + `","version":"1.0.0","filex":">=0.50.0","permissions":["files:read"],` +
				`"wasm":{"url":"` + srv.URL + `/` + full + `/releases/download/{tag}/app.wasm","sha256":"` + hexSum(demoWasm) + `"},"actions":[{"id":"run"}]}`)
		}
		switch p := r.URL.Path; p {
		case "/repos/" + demoRepo:
			repoJSON(demoRepo, demoRepoID, brfTechID, "Organization")
		case "/repos/" + otherRepo:
			repoJSON(otherRepo, 7002, 99, "User")
		case "/repos/" + demoRepo + "/git/ref/tags/v1.0.0":
			_, _ = w.Write([]byte(`{"ref":"refs/tags/v1.0.0","object":{"sha":"` + demoCommit + `","type":"commit"}}`))
		case "/repos/" + otherRepo + "/git/ref/tags/v1.0.0":
			_, _ = w.Write([]byte(`{"ref":"refs/tags/v1.0.0","object":{"sha":"` + otherCommit + `","type":"commit"}}`))
		case "/repos/" + demoRepo + "/license", "/repos/" + otherRepo + "/license":
			_, _ = w.Write([]byte(`{"path":"LICENSE","license":{"spdx_id":"MIT"}}`))
		case "/" + demoRepo + "/" + demoCommit + "/filex-app.json":
			_, _ = w.Write(manifest(demoRepo, "demo"))
		case "/" + otherRepo + "/" + otherCommit + "/filex-app.json":
			_, _ = w.Write(manifest(otherRepo, "other"))
		case "/" + demoRepo + "/releases/download/v1.0.0/app.wasm", "/" + otherRepo + "/releases/download/v1.0.0/app.wasm":
			_, _ = w.Write(demoWasm)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// validators stands in for filex 0.51.0 and 0.50.0: both answer with the
// resolver's own pins.
type validators struct{}

func (validators) DryRun(ctx context.Context, req domain.DryRunRequest) ([]domain.DryRunResult, []domain.CheckResult, error) {
	now := time.Now()
	ok := true
	var results []domain.DryRunResult
	var checks []domain.CheckResult
	for label, version := range map[string]string{"latest": "0.51.0", "floor": "0.50.0"} {
		var rows []domain.PermissionRow
		for _, p := range req.ExpectPermissions {
			rows = append(rows, domain.PermissionRow{ID: p, Label: p})
		}
		results = append(results, domain.DryRunResult{Validator: label, FilexVersion: version, OK: true, Kind: req.Kind, CompatOK: &ok,
			WasmSHA256: req.ExpectWasmSHA256, Permissions: rows, Raw: json.RawMessage(`{}`)})
		checks = append(checks, domain.CheckResult{Kind: domain.CheckDryRun, Validator: label, Status: domain.CheckPassed, StartedAt: now, EndedAt: &now})
	}
	return results, checks, nil
}

type world struct {
	pool   *pgxpool.Pool
	svc    *pipeline.Service
	runner *pipeline.Runner
	pub    *index.Releaser
	op     pipeline.Actor
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := dbtest.Migrated(t)
	keys, err := index.LoadKeys(ctx, pool, config.KeyConfig{DemoKeys: true}, "development", log)
	if err != nil || keys.Index == nil || keys.Artifact == nil {
		t.Fatalf("demo keys: %v", err)
	}
	srv := gitHub(t)
	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	u, _ := url.Parse(srv.URL)
	hc := safehttp.New(safehttp.Config{AllowHosts: []string{u.Host}, RootCAs: roots, Addresses: func(netip.Addr) error { return nil }})
	gh := ghapi.New(ghapi.Options{HTTP: hc, APIURL: srv.URL, RawURL: srv.URL})
	sink := &media.Sink{DB: pool, MaxBytes: 5 << 20}
	res := resolve.New(resolve.Options{GitHub: gh, Media: sink})
	pub := &index.Releaser{Pool: pool, Keys: keys, PublicURL: "https://fapps.example", TTL: 60 * 24 * time.Hour, RefreshBefore: 30 * 24 * time.Hour, Log: log}
	runner := &pipeline.Runner{Pool: pool, Resolver: res, DryRunner: validators{}, Index: pub, Log: log, Validators: 2,
		Permissions: pipeline.PermissionRule{Normalize: resolve.NormalizePermissions, IsDerived: resolve.IsDerivedPermission}}
	svc := &pipeline.Service{Pool: pool, Repos: res, Keys: keys, Index: pub, Log: log}
	op, err := svc.OperatorActor(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return &world{pool: pool, svc: svc, runner: runner, pub: pub, op: op}
}

func (w *world) officialBRFTech(t *testing.T) store.Publisher {
	t.Helper()
	p, created, err := w.svc.EnsureOfficialPublisher(context.Background(), pipeline.OfficialPublisher{Actor: w.op, ID: "brf-tech", Name: "BRF Tech",
		Owner: pipeline.Owner{ID: brfTechID, Login: "BRF-Tech", Type: "Organization"}})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Official || !p.Verified || p.Status != "active" || p.GitHubOwnerID != brfTechID {
		t.Fatalf("publisher %+v (created %v)", p, created)
	}
	return p
}

func listing(publisher string) *domain.OperatorImport {
	return &domain.OperatorImport{
		Listing: domain.Consent{Publisher: publisher, Categories: []string{"developer"}, Summary: domain.Text{"en": "A demo app."},
			Description: "description.md", Icon: "icon.png", Screenshots: []domain.ConsentShot{{Path: "shot.png", Caption: domain.Text{"en": "The demo"}}}},
		Description: "# Demo\n\nIt does a demo.\n",
		Files:       map[string][]byte{"icon.png": pngImage(256, 256), "shot.png": pngImage(1280, 800)},
		Source:      "seed/official/demo/listing.json sha256:11",
	}
}

// drain works the queue the way the server's workers do.
func (w *world) drain(t *testing.T) {
	t.Helper()
	for i := 0; i < 20; i++ {
		did, err := w.runner.Step(context.Background(), store.JobResolve, store.JobDryRun)
		if err != nil {
			t.Fatal(err)
		}
		if !did {
			return
		}
	}
	t.Fatal("the queue did not drain")
}

func refusalCode(err error) string {
	var r *pipeline.Refusal
	if errors.As(err, &r) {
		return r.Code
	}
	return ""
}

func submissions(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM submissions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func auditData(t *testing.T, pool *pgxpool.Pool, action string) map[string]any {
	t.Helper()
	var raw []byte
	var role string
	if err := pool.QueryRow(context.Background(), `SELECT data, actor_role FROM audit_log WHERE action = $1 ORDER BY id DESC LIMIT 1`, action).Scan(&raw, &role); err != nil {
		t.Fatalf("audit %s: %v", action, err)
	}
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	m["_role"] = role
	return m
}

func TestOperatorImport_TheWholePipelineThenApprovalAndTheSignedIndex(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.officialBRFTech(t)

	sub, err := w.svc.Import(ctx, pipeline.ImportRequest{Actor: w.op, Publisher: "brf-tech", Repo: demoRepo, Ref: "v1.0.0", Listing: listing("brf-tech")})
	if err != nil {
		t.Fatal(err)
	}
	if sub.Status != "queued" || sub.SubmittedBy != w.op.AccountID {
		t.Fatalf("submission %+v", sub)
	}
	w.drain(t)
	sub, _ = store.GetSubmission(ctx, w.pool, sub.ID)
	checks, _ := store.ChecksOf(ctx, w.pool, sub.ID, sub.Attempt)
	var got []string
	for i, c := range checks {
		got = append(got, string(c.Kind)+"="+string(c.Status))
		if c.Kind == domain.CheckConsent && c.Message != pipeline.OperatorConsent {
			t.Errorf("consent check %+v", c)
		}
		if c.Kind == domain.CheckRepoAccess && !strings.Contains(string(c.Data), "not asked: "+pipeline.OperatorConsent) {
			t.Errorf("repo_access check %d %s", i, c.Data)
		}
	}
	if checks[0].Message != pipeline.OperatorConsent {
		t.Errorf("the synchronous repo_access check %+v", checks[0])
	}
	want := "repo_access=passed repo_access=passed tag=passed manifest=passed consent=passed artifacts=passed media=passed policy=passed dry_run=passed dry_run=passed"
	if sub.Status != "needs_review" || strings.Join(got, " ") != want {
		t.Fatalf("status %s (%s %s)\nchecks %s\nwant   %s", sub.Status, sub.FailureCode, sub.FailureMessage, strings.Join(got, " "), want)
	}

	res, err := w.svc.Approve(ctx, sub.ID, pipeline.Decision{Actor: w.op, Note: pipeline.OperatorConsent})
	if err != nil {
		t.Fatal(err)
	}
	if res.Serial == 0 || res.Version.Version != "1.0.0" || res.Version.Commit != demoCommit || res.Version.Wasm == nil || res.Version.Wasm.Sig == "" {
		t.Fatalf("approval %+v", res)
	}
	app, err := store.GetAppByName(ctx, w.pool, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if app.PublisherID != "brf-tech" || app.Summary["en"] != "A demo app." || app.IconSHA256 != hexSum(listing("brf-tech").Files["icon.png"]) ||
		strings.Join(app.Categories, ",") != "developer" || !strings.Contains(app.DescriptionMD, "It does a demo.") {
		t.Errorf("app listing %+v", app)
	}
	rel, err := w.pub.Latest(ctx)
	if err != nil || rel == nil || rel.Serial != res.Serial || !bytes.Contains(rel.Body, []byte(`"demo"`)) {
		t.Fatalf("index %v %+v", err, rel)
	}

	create := auditData(t, w.pool, "submission.create")
	if create["operator_import"] != true || create["consent"] != pipeline.OperatorConsent || create["_role"] != audit.RoleAdmin {
		t.Errorf("submission.create audit %v", create)
	}
	approve := auditData(t, w.pool, "submission.approve")
	if approve["operator_import"] != true || approve["self_review"] != true || approve["consent"] != pipeline.OperatorConsent {
		t.Errorf("submission.approve audit %v (Q9: an admin approving its own publisher is flagged)", approve)
	}
	if ok, broken, err := audit.Verify(ctx, w.pool); err != nil || !ok {
		t.Errorf("audit chain broken at %d: %v", broken, err)
	}
	// The same tag again is refused: it is listed.
	if _, err := w.svc.Import(ctx, pipeline.ImportRequest{Actor: w.op, Publisher: "brf-tech", Repo: demoRepo, Ref: "v1.0.0", Listing: listing("brf-tech")}); refusalCode(err) != "version_exists" {
		t.Errorf("a listed tag again: %v", err)
	}
}

// Red: the rule's first half. A publisher that is not official, or not
// verified, cannot be imported this way: nothing is queued.
func TestOperatorImport_RefusesAPublisherThatIsNotOfficialAndVerified(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.officialBRFTech(t)
	for name, patch := range map[string]store.PublisherPatch{
		"not official": {Official: ptr(false)},
		"not verified": {Official: ptr(true), Verified: ptr(false)},
	} {
		if _, _, err := w.svc.UpdatePublisher(ctx, "brf-tech", patch, w.op); err != nil {
			t.Fatal(err)
		}
		_, err := w.svc.Import(ctx, pipeline.ImportRequest{Actor: w.op, Publisher: "brf-tech", Repo: demoRepo, Ref: "v1.0.0", Listing: listing("brf-tech")})
		if refusalCode(err) != "publisher_not_official" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if n := submissions(t, w.pool); n != 0 {
		t.Errorf("%d submissions queued", n)
	}
}

// Red: the rule's second half. The repository's owner must be the
// publisher's bound GitHub owner; the listing must name the publisher.
func TestOperatorImport_RefusesARepositoryOfAnotherOwner(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.officialBRFTech(t)
	_, err := w.svc.Import(ctx, pipeline.ImportRequest{Actor: w.op, Publisher: "brf-tech", Repo: otherRepo, Ref: "v1.0.0", Listing: listing("brf-tech")})
	if refusalCode(err) != "owner_mismatch" {
		t.Errorf("another owner's repository: %v", err)
	}
	_, err = w.svc.Import(ctx, pipeline.ImportRequest{Actor: w.op, Publisher: "brf-tech", Repo: demoRepo, Ref: "v1.0.0", Listing: listing("someone")})
	if refusalCode(err) != "consent_publisher" {
		t.Errorf("a listing for another publisher: %v", err)
	}
	if n := submissions(t, w.pool); n != 0 {
		t.Errorf("%d submissions queued", n)
	}
	// An existing publisher is not rebound to another owner.
	_, _, err = w.svc.EnsureOfficialPublisher(ctx, pipeline.OfficialPublisher{Actor: w.op, ID: "brf-tech", Name: "BRF Tech",
		Owner: pipeline.Owner{ID: 99, Login: "someone", Type: "User"}})
	if refusalCode(err) != "owner_mismatch" {
		t.Errorf("rebinding brf-tech: %v", err)
	}
}

// The resolve checks consent by ownership again, with the publisher as the
// database says when it runs; approval checks it once more.
func TestOperatorImport_TheResolveAndTheApprovalCheckTheStandingAgain(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.officialBRFTech(t)
	sub, err := w.svc.Import(ctx, pipeline.ImportRequest{Actor: w.op, Publisher: "brf-tech", Repo: demoRepo, Ref: "v1.0.0", Listing: listing("brf-tech")})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.svc.UpdatePublisher(ctx, "brf-tech", store.PublisherPatch{Official: ptr(false)}, w.op); err != nil {
		t.Fatal(err)
	}
	w.drain(t)
	sub, _ = store.GetSubmission(ctx, w.pool, sub.ID)
	if sub.Status != "failed" || sub.FailureCode != resolve.CodePublisherNotOfficial {
		t.Errorf("resolve after the publisher lost its standing: %s %s %s", sub.Status, sub.FailureCode, sub.FailureMessage)
	}

	if _, _, err := w.svc.UpdatePublisher(ctx, "brf-tech", store.PublisherPatch{Official: ptr(true)}, w.op); err != nil {
		t.Fatal(err)
	}
	sub, err = w.svc.Import(ctx, pipeline.ImportRequest{Actor: w.op, Publisher: "brf-tech", Repo: demoRepo, Ref: "v1.0.0", Listing: listing("brf-tech")})
	if err != nil {
		t.Fatal(err)
	}
	w.drain(t)
	if s, _ := store.GetSubmission(ctx, w.pool, sub.ID); s.Status != "needs_review" {
		t.Fatalf("second import: %s %s %s", s.Status, s.FailureCode, s.FailureMessage)
	}
	if _, _, err := w.svc.UpdatePublisher(ctx, "brf-tech", store.PublisherPatch{Verified: ptr(false)}, w.op); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Approve(ctx, sub.ID, pipeline.Decision{Actor: w.op}); refusalCode(err) != "publisher_not_official" {
		t.Errorf("approval after the publisher lost its standing: %v", err)
	}
}

// The web path is unchanged: a submission through Submit carries no
// operator listing, so a tag without .filex-apps.json fails consent_missing
// even for the official publisher.
func TestOperatorImport_TheWebPathStillNeedsTheConsentFile(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.officialBRFTech(t)
	member := pipeline.Actor{AccountID: w.op.AccountID, Role: audit.RolePublisher}
	sub, err := w.svc.Submit(ctx, pipeline.SubmitRequest{Actor: member, Token: "user-token", Publisher: "brf-tech", Repo: demoRepo, Ref: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := store.OperatorListing(ctx, w.pool, sub.ID); err != nil || raw != nil {
		t.Fatalf("a web submission carries an operator listing: %s %v", raw, err)
	}
	w.drain(t)
	sub, _ = store.GetSubmission(ctx, w.pool, sub.ID)
	if sub.Status != "failed" || sub.FailureCode != resolve.CodeConsentMissing {
		t.Errorf("web submission without .filex-apps.json: %s %s %s", sub.Status, sub.FailureCode, sub.FailureMessage)
	}
	create := auditData(t, w.pool, "submission.create")
	if _, ok := create["operator_import"]; ok {
		t.Errorf("a web submission's audit row says operator_import: %v", create)
	}
}

func ptr[T any](v T) *T { return &v }

// profiles stands in for the GitHub profile reader: it mirrors an avatar
// through the real media sink, like cmd/server's githubProfiles.
type profiles struct {
	sink  *media.Sink
	desc  string
	fail  error
	calls []string
}

func (p *profiles) Profile(ctx context.Context, ownerID int64, login string) (pipeline.Profile, error) {
	p.calls = append(p.calls, login)
	if p.fail != nil {
		return pipeline.Profile{}, p.fail
	}
	avatar := pngImage(64, 64)
	ref := domain.MediaRef{Role: "avatar", SHA256: hexSum(avatar), Type: "image/png", Width: 64, Height: 64}
	if err := p.sink.Put(ctx, ref, "https://avatars.githubusercontent.com/u/1?s=256", avatar); err != nil {
		return pipeline.Profile{}, err
	}
	return pipeline.Profile{Description: p.desc, AvatarSHA256: ref.SHA256}, nil
}

// A publisher's GitHub profile is read when it becomes active (the web
// path's activation and the operator command alike) and again by the
// operator command; a reader that fails leaves the publisher as it is.
func TestPublisherProfile_ReadWhenThePublisherBecomesActive(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	prof := &profiles{sink: &media.Sink{DB: w.pool, MaxBytes: 5 << 20}, desc: "Home of filex."}
	w.svc.Profiles = prof

	p := w.officialBRFTech(t)
	if p.Description != "Home of filex." || p.AvatarSHA256 != hexSum(pngImage(64, 64)) || p.AvatarType != "image/png" || len(prof.calls) != 1 {
		t.Fatalf("after creation: %+v, calls %v", p, prof.calls)
	}
	prof.desc = "Home of filex, and its apps."
	p = w.officialBRFTech(t)
	if p.Description != "Home of filex, and its apps." || len(prof.calls) != 2 {
		t.Errorf("the operator command reads it again: %+v, calls %v", p, prof.calls)
	}
	if auditData(t, w.pool, "publisher.profile")["description"] != "Home of filex, and its apps." {
		t.Error("no publisher.profile audit row")
	}

	// The web path: a pending publisher an administrator activates.
	if _, err := w.pool.Exec(ctx, `INSERT INTO publishers (id, name, github_owner, github_owner_id, owner_type, status)
		VALUES ('alice', 'Alice', 'alice', 31337, 'User', 'pending')`); err != nil {
		t.Fatal(err)
	}
	prof.desc = "Alice's tools."
	active := "active"
	got, _, err := w.svc.UpdatePublisher(ctx, "alice", store.PublisherPatch{Status: &active}, w.op)
	if err != nil || got.Description != "Alice's tools." || prof.calls[len(prof.calls)-1] != "alice" {
		t.Errorf("activation: %+v %v calls %v", got, err, prof.calls)
	}
	verified := true
	n := len(prof.calls)
	if _, _, err := w.svc.UpdatePublisher(ctx, "alice", store.PublisherPatch{Verified: &verified}, w.op); err != nil || len(prof.calls) != n {
		t.Errorf("a change that is not an activation reads no profile: %v, calls %v", err, prof.calls)
	}

	// Best effort: GitHub failing does not undo an activation.
	if _, err := w.pool.Exec(ctx, `INSERT INTO publishers (id, name, github_owner, github_owner_id, owner_type, status)
		VALUES ('bob', 'Bob', 'bob', 4242, 'User', 'pending')`); err != nil {
		t.Fatal(err)
	}
	prof.fail = errors.New("GitHub is down")
	got, _, err = w.svc.UpdatePublisher(ctx, "bob", store.PublisherPatch{Status: &active}, w.op)
	if err != nil || got.Status != "active" || got.Description != "" || got.AvatarSHA256 != "" {
		t.Errorf("a failing reader: %+v %v", got, err)
	}
}
