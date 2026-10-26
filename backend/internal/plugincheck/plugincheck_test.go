package plugincheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/safehttp"
)

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

var build = []byte("\x7fELF the linux/amd64 build of myfs")

// fakeValidator stands in for `filex plugin-validator` on a spool: it beats,
// takes every job that arrives and answers it with answer(job, bytes it
// read). It never runs anything.
type fakeValidator struct {
	dir    string
	filex  string
	plat   string
	answer func(j job, got []byte) *Result
	beat   bool

	mu   sync.Mutex
	jobs []job
}

func newFakeValidator(t *testing.T) *fakeValidator {
	t.Helper()
	dir := t.TempDir()
	for _, d := range []string{"in", "out"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &fakeValidator{dir: dir, filex: "v0.55.0 (abc, 2026-10-08T00:00:00Z)", plat: "linux/amd64", beat: true}
}

func (f *fakeValidator) writeStatus(t *testing.T, at time.Time) {
	t.Helper()
	b, _ := json.Marshal(Status{Protocol: Protocol, FilexVersion: f.filex, Platform: f.plat, HeartbeatAt: at})
	if err := os.WriteFile(filepath.Join(f.dir, "status.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// serve answers jobs until ctx ends.
func (f *fakeValidator) serve(ctx context.Context, t *testing.T) {
	done := map[string]bool{}
	for ctx.Err() == nil {
		entries, _ := os.ReadDir(filepath.Join(f.dir, "in"))
		for _, e := range entries {
			id, ok := strings.CutSuffix(e.Name(), ".json")
			if !ok || done[id] || !ValidID(id) {
				continue
			}
			done[id] = true
			b, err := os.ReadFile(filepath.Join(f.dir, "in", e.Name()))
			if err != nil {
				continue
			}
			var j job
			_ = json.Unmarshal(b, &j)
			got, _ := os.ReadFile(filepath.Join(f.dir, "in", id+".bin"))
			f.mu.Lock()
			f.jobs = append(f.jobs, j)
			f.mu.Unlock()
			out := f.answer(j, got)
			ob, _ := json.Marshal(out)
			// Written aside and renamed into place, as filex's validator does:
			// the store never reads half a result.
			final := filepath.Join(f.dir, "out", id+".json")
			_ = os.WriteFile(final+".partial", ob, 0o644)
			_ = os.Rename(final+".partial", final)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// verified answers like filex does for a plugin whose claims hold.
func (f *fakeValidator) verified(version string) func(j job, got []byte) *Result {
	return func(j job, got []byte) *Result {
		rep := map[string]any{"verified": true, "scratch": "selftest", "results": []map[string]any{
			{"name": "list", "status": "pass"}, {"name": "read", "status": "pass"}, {"name": "write", "status": "pass"},
			{"name": "presign", "status": "skip"}, {"name": "watch", "status": "skip"},
		}}
		rb, _ := json.Marshal(rep)
		r := &Result{Protocol: Protocol, ID: j.ID, SHA256: sum(got), FilexVersion: f.filex, Platform: f.plat, OK: true, Conformance: rb}
		r.Describe = &Described{Protocol: 1, Name: "myfs", Version: version, Label: "My FS", Capabilities: map[string]bool{"write": true, "delete": true, "range": false}}
		return r
	}
}

func fetchOf(b []byte) Fetcher {
	return func(_ context.Context, _ string, max int64, w io.Writer) (string, int64, error) {
		if int64(len(b)) > max {
			return "", 0, safehttp.ErrTooLarge
		}
		n, err := w.Write(b)
		return sum(b), int64(n), err
	}
}

func runner(f *fakeValidator, fetch Fetcher) *Runner {
	return New(Options{Validators: []Validator{{Label: "latest", Dir: f.dir}}, Fetch: fetch, Poll: 5 * time.Millisecond, Timeout: 5 * time.Second})
}

func request() domain.DryRunRequest {
	return domain.DryRunRequest{Kind: domain.KindStorage, Name: "myfs", Version: "1.3.0", FilexRange: ">=0.55.0",
		Binaries: map[string]domain.Artifact{
			"linux/amd64": {URL: "https://github.com/a/b/releases/download/v1.3.0/myfs-linux-amd64", SHA256: sum(build)},
			"linux/arm64": {URL: "https://github.com/a/b/releases/download/v1.3.0/myfs-linux-arm64", SHA256: strings.Repeat("b", 64)},
		}}
}

func start(t *testing.T, f *fakeValidator) {
	t.Helper()
	f.writeStatus(t, time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go f.serve(ctx, t)
}

func TestDryRun_AVerifiedBuildPasses(t *testing.T) {
	f := newFakeValidator(t)
	f.answer = f.verified("1.3.0")
	start(t, f)
	results, checks, err := runner(f, fetchOf(build)).DryRun(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].Status != domain.CheckPassed || checks[0].Validator != "latest" {
		t.Fatalf("checks %+v", checks)
	}
	if len(results) != 1 || !results[0].OK || results[0].FilexVersion != "0.55.0" || results[0].Kind != domain.KindStorage {
		t.Fatalf("results %+v", results)
	}
	run := results[0].Storage
	if run == nil || run.Platform != "linux/amd64" || run.BinarySHA256 != sum(build) || run.Driver != "myfs" {
		t.Fatalf("run %+v", run)
	}
	s := run.Summary
	if s == nil || !s.Verified || s.Passed != 3 || s.Skipped != 2 || s.Failed != 0 || s.Filex != "0.55.0" ||
		strings.Join(s.Capabilities, ",") != "delete,write" {
		t.Fatalf("summary %+v", s)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.jobs) != 1 || f.jobs[0].SHA256 != sum(build) || f.jobs[0].Version != "1.3.0" {
		t.Fatalf("job %+v", f.jobs)
	}
	// The store removes its own inputs once the result is read.
	if left, _ := os.ReadDir(filepath.Join(f.dir, "in")); len(left) != 0 {
		t.Errorf("inputs left behind: %v", left)
	}
}

func TestDryRun_ABuildThatIsNotThePinnedOneIsNeverHandedToTheValidator(t *testing.T) {
	f := newFakeValidator(t)
	f.answer = f.verified("1.3.0")
	start(t, f)
	_, checks, err := runner(f, fetchOf([]byte("another build"))).DryRun(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if checks[0].Status != domain.CheckFailed || checks[0].Code != CodeSHA256Mismatch {
		t.Fatalf("checks %+v", checks)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.jobs) != 0 {
		t.Fatalf("a job was written for bytes that are not the pinned build: %+v", f.jobs)
	}
}

func TestDryRun_ABuildThatDescribesAnotherVersionFails(t *testing.T) {
	f := newFakeValidator(t)
	f.answer = f.verified("1.2.9")
	start(t, f)
	_, checks, err := runner(f, fetchOf(build)).DryRun(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if checks[0].Status != domain.CheckFailed || checks[0].Code != CodeVersionMismatch {
		t.Fatalf("checks %+v", checks)
	}
}

func TestDryRun_ABuildThatDescribesAnotherPluginFails(t *testing.T) {
	f := newFakeValidator(t)
	f.answer = f.verified("1.3.0")
	start(t, f)
	req := request()
	req.Name = "otherfs"
	_, checks, err := runner(f, fetchOf(build)).DryRun(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if checks[0].Status != domain.CheckFailed || checks[0].Code != CodeNameMismatch {
		t.Fatalf("checks %+v", checks)
	}
}

func TestDryRun_APluginWithoutASelftestIsNotListed(t *testing.T) {
	f := newFakeValidator(t)
	verified := f.verified("1.3.0")
	f.answer = func(j job, got []byte) *Result {
		r := verified(j, got)
		r.Conformance = nil
		return r
	}
	start(t, f)
	_, checks, err := runner(f, fetchOf(build)).DryRun(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if checks[0].Status != domain.CheckFailed || checks[0].Code != CodeUnverified {
		t.Fatalf("checks %+v", checks)
	}
}

func TestDryRun_AFailedProbeFailsTheBuild(t *testing.T) {
	f := newFakeValidator(t)
	f.answer = func(j job, got []byte) *Result {
		rb, _ := json.Marshal(map[string]any{"verified": false, "results": []map[string]any{
			{"name": "list", "status": "pass"}, {"name": "write", "status": "fail", "detail": "Write reported success, nothing was stored"},
		}})
		r := f.verified("1.3.0")(j, got)
		r.Conformance = rb
		return r
	}
	start(t, f)
	_, checks, err := runner(f, fetchOf(build)).DryRun(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if checks[0].Code != CodeConformanceFailed || !strings.Contains(checks[0].Message, "nothing was stored") {
		t.Fatalf("checks %+v", checks)
	}
}

func TestDryRun_TheValidatorsOwnRefusalIsKept(t *testing.T) {
	f := newFakeValidator(t)
	f.answer = func(j job, got []byte) *Result {
		return &Result{Protocol: Protocol, ID: j.ID, SHA256: sum(got), FilexVersion: f.filex, Platform: f.plat,
			Code: "handshake", Message: "plugin did not print its handshake line within 20s"}
	}
	start(t, f)
	_, checks, err := runner(f, fetchOf(build)).DryRun(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if checks[0].Code != "handshake" || !strings.Contains(checks[0].Message, "handshake line") {
		t.Fatalf("checks %+v", checks)
	}
}

func TestDryRun_AValidatorThatIsNotRunningIsRetried(t *testing.T) {
	f := newFakeValidator(t)
	f.writeStatus(t, time.Now().Add(-10*time.Minute))
	_, _, err := runner(f, fetchOf(build)).DryRun(context.Background(), request())
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("a stale heartbeat must be a retryable error, got %v", err)
	}
}

func TestDryRun_AValidatorOutsideTheRangeIsSkipped(t *testing.T) {
	f := newFakeValidator(t)
	f.filex = "v0.54.0"
	f.answer = f.verified("1.3.0")
	start(t, f)
	results, checks, err := runner(f, fetchOf(build)).DryRun(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 || len(checks) != 2 || checks[0].Code != CodeOutOfRange || checks[1].Code != CodeNoValidatorInRange {
		t.Fatalf("results %+v checks %+v", results, checks)
	}
}

func TestDryRun_NoBuildForTheValidatorsPlatformFails(t *testing.T) {
	f := newFakeValidator(t)
	f.plat = "linux/riscv64"
	f.answer = f.verified("1.3.0")
	start(t, f)
	_, checks, err := runner(f, fetchOf(build)).DryRun(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if checks[0].Code != CodeNoBuild {
		t.Fatalf("checks %+v", checks)
	}
}

func TestDryRun_ADownloadTheGuardRefusesIsAFailedCheck(t *testing.T) {
	f := newFakeValidator(t)
	f.answer = f.verified("1.3.0")
	start(t, f)
	refused := func(context.Context, string, int64, io.Writer) (string, int64, error) {
		return "", 0, safehttp.ErrHostNotAllowed
	}
	_, checks, err := runner(f, refused).DryRun(context.Background(), request())
	if err != nil {
		t.Fatal(err)
	}
	if checks[0].Code != CodeFetchFailed {
		t.Fatalf("checks %+v", checks)
	}
	broken := func(context.Context, string, int64, io.Writer) (string, int64, error) {
		return "", 0, errors.New("connection reset")
	}
	if _, _, err := runner(f, broken).DryRun(context.Background(), request()); err == nil {
		t.Fatal("a network failure is the pipeline's to retry")
	}
}

// A validator's files are read up to a cap: a heartbeat or a result larger
// than the store reads is refused, never read whole.
func TestStatuses_AHeartbeatLargerThanTheCapIsNotRead(t *testing.T) {
	f := newFakeValidator(t)
	b, _ := json.Marshal(Status{Protocol: Protocol, FilexVersion: f.filex, Platform: f.plat, HeartbeatAt: time.Now(),
		Busy: strings.Repeat("a", maxStatusBytes)})
	if err := os.WriteFile(filepath.Join(f.dir, "status.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	h := runner(f, fetchOf(build)).Statuses()
	if len(h) != 1 || h[0].Reachable || !strings.Contains(h[0].Error, "larger than the store reads") {
		t.Fatalf("an oversized heartbeat was read: %+v", h)
	}
}

func TestDryRun_AResultLargerThanTheCapIsNotRead(t *testing.T) {
	f := newFakeValidator(t)
	verified := f.verified("1.3.0")
	f.answer = func(j job, got []byte) *Result {
		r := verified(j, got)
		r.Message = strings.Repeat("m", maxResultBytes)
		return r
	}
	start(t, f)
	_, _, err := runner(f, fetchOf(build)).DryRun(context.Background(), request())
	if err == nil || !strings.Contains(err.Error(), "larger than the store reads") {
		t.Fatalf("an oversized result was read: %v", err)
	}
}
