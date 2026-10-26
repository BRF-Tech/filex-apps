// Package plugincheck implements pipeline.DryRunner for storage plugins: it
// has the store's PLUGIN VALIDATORS run a build's conformance and compares
// what they report with the resolver's pins (docs/ARCHITECTURE.md "Storage
// plugins").
//
// # Why a validator and not the store
//
// An app's dry run is a real filex's install review, so the rules a manifest
// must satisfy are written once - in filex. A storage plugin's rules are
// filex's too: the handshake, the describe it accepts, and the conformance
// probes it runs at every start of a plugin (internal/plugin in filex). So
// the run is filex's own code: `filex plugin-validator` (filex 0.55 and
// later), in a container of its own.
//
// # Why a spool and not an address
//
// A build is a NATIVE program the store has never seen run. Its validator
// container has no network at all (`network_mode: none`), no capability but
// the four it needs to drop the plugin to an unprivileged user, a read-only
// root, a memory, CPU and process ceiling; the plugin runs as `nobody`, the
// harness as root inside the container. A container with no network cannot be
// called, so the store and the validator share one directory:
//
//	<spool>/status.json      the validator's heartbeat: its filex, its platform
//	<spool>/in/<id>.bin      the build, written by the store (held to its sha256 on both sides)
//	<spool>/in/<id>.json     the job, written last: its arrival is the job's
//	<spool>/out/<id>.json    the validator's result
//
// `in/` belongs to the store's user and `out/` to the harness: the plugin under
// test can write neither, so it can neither leave a job of its own nor forge a
// result. The store removes its own inputs once the result is read; the
// validator prunes old results.
package plugincheck

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/filexrange"
	"github.com/brf-tech/filex-apps/backend/internal/github"
	"github.com/brf-tech/filex-apps/backend/internal/pipeline"
	"github.com/brf-tech/filex-apps/backend/internal/safehttp"
)

// Protocol is the spool's version; a status or a result of another is not
// read.
const Protocol = 1

// Codes the store writes itself (a validator's own refusals are kept with
// their code).
const (
	CodeOutOfRange         = "out_of_range"
	CodeNoBuild            = "no_build_for_platform"
	CodeSHA256Mismatch     = "sha256_mismatch"
	CodeFetchFailed        = "fetch_failed"
	CodeVersionMismatch    = "version_mismatch"
	CodeNameMismatch       = "name_mismatch"
	CodeUnverified         = "conformance_unverified"
	CodeConformanceFailed  = "conformance_failed"
	CodeAnswerIncomplete   = "answer_incomplete"
	CodeRangeInvalid       = "range_invalid"
	CodeNoValidatorInRange = "no_validator_in_range"
)

// Validator is one plugin validator: its label (`latest`) and the spool
// directory it shares with the store.
type Validator struct {
	Label string
	Dir   string
}

// Fetcher streams a build into w and answers its sha256 and size (the
// guarded client: github.Client.DownloadTo).
type Fetcher func(ctx context.Context, url string, max int64, w io.Writer) (sha256 string, size int64, err error)

// Options builds a Runner.
type Options struct {
	Validators []Validator
	Fetch      Fetcher
	// MaxBinary caps one build (FAPPS_MAX_BINARY_MB; default 256 MiB).
	MaxBinary int64
	// Timeout bounds one validator's whole run of a build, from the job
	// written to the result read (default 4 minutes: the validator's own caps
	// are 20 s to start and 90 s of probes).
	Timeout time.Duration
	// Poll is how often the result is looked for (default 500 ms).
	Poll time.Duration
	// Stale is how old a heartbeat may be before the validator is taken as
	// not running (default 90 s).
	Stale time.Duration
	Now   func() time.Time
}

// Runner implements pipeline.DryRunner for storage plugins.
type Runner struct {
	o    Options
	sems map[string]chan struct{}
}

var _ pipeline.DryRunner = (*Runner)(nil)

// New builds a Runner.
func New(o Options) *Runner {
	if o.MaxBinary <= 0 {
		o.MaxBinary = 256 << 20
	}
	if o.Timeout <= 0 {
		o.Timeout = 4 * time.Minute
	}
	if o.Poll <= 0 {
		o.Poll = 500 * time.Millisecond
	}
	if o.Stale <= 0 {
		o.Stale = 90 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	r := &Runner{o: o, sems: map[string]chan struct{}{}}
	for _, v := range o.Validators {
		r.sems[v.Label] = make(chan struct{}, 1)
	}
	return r
}

// GitHubFetcher is the production Fetcher: the guarded client's stream.
func GitHubFetcher(gh *github.Client) Fetcher {
	return func(ctx context.Context, url string, max int64, w io.Writer) (string, int64, error) {
		a, err := gh.DownloadTo(ctx, url, max, w)
		if err != nil {
			return "", 0, err
		}
		return a.SHA256, a.Size, nil
	}
}

// Status is a validator's heartbeat (<spool>/status.json), as the validator
// writes it and the admin page shows it.
type Status struct {
	Protocol     int       `json:"protocol"`
	FilexVersion string    `json:"filex_version"`
	Platform     string    `json:"platform"`
	HeartbeatAt  time.Time `json:"heartbeat_at"`
	Busy         string    `json:"busy,omitempty"`
}

// Health is one validator's state for GET /api/v1/admin/validators.
type Health struct {
	Label        string `json:"label"`
	Kind         string `json:"kind"` // "storage"
	Reachable    bool   `json:"reachable"`
	FilexVersion string `json:"filex_version,omitempty"`
	Platform     string `json:"platform,omitempty"`
	Error        string `json:"error,omitempty"`
}

// Statuses reads every validator's heartbeat.
func (r *Runner) Statuses() []Health {
	out := make([]Health, 0, len(r.o.Validators))
	for _, v := range r.o.Validators {
		h := Health{Label: v.Label, Kind: string(domain.KindStorage)}
		st, err := r.status(v)
		if err != nil {
			h.Error = err.Error()
		} else {
			h.Reachable, h.FilexVersion, h.Platform = true, st.FilexVersion, st.Platform
		}
		out = append(out, h)
	}
	return out
}

// The most of a validator's file the store reads. A heartbeat is a few
// hundred bytes; a result carries the plugin's describe and filex's
// conformance report, tens of KiB. A file over its cap is not read on, so
// what a validator (or the plugin's strings inside its result) writes cannot
// make the store allocate without bound.
const (
	maxStatusBytes = 1 << 20
	maxResultBytes = 4 << 20
)

// errTooLarge is a validator's file over its cap.
var errTooLarge = errors.New("larger than the store reads")

// readCapped reads path, a regular file of at most max bytes.
func readCapped(path string, max int64) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is %w (%d KiB)", filepath.Base(path), errTooLarge, max>>10)
	}
	return b, nil
}

// status reads a validator's heartbeat and refuses one that is missing,
// unreadable, too large, of another protocol or older than Stale.
func (r *Runner) status(v Validator) (*Status, error) {
	b, err := readCapped(filepath.Join(v.Dir, "status.json"), maxStatusBytes)
	if errors.Is(err, errTooLarge) {
		return nil, fmt.Errorf("plugin validator %s: its status is not one this store reads (%v)", v.Label, err)
	}
	if err != nil {
		return nil, fmt.Errorf("plugin validator %s is not running (no status in %s)", v.Label, v.Dir)
	}
	var st Status
	if err := json.Unmarshal(b, &st); err != nil || st.Protocol != Protocol || st.Platform == "" || strings.TrimSpace(st.FilexVersion) == "" {
		return nil, fmt.Errorf("plugin validator %s: its status is not one this store reads", v.Label)
	}
	if age := r.o.Now().Sub(st.HeartbeatAt); age > r.o.Stale || age < -r.o.Stale {
		return nil, fmt.Errorf("plugin validator %s is not running (last heartbeat %s)", v.Label, st.HeartbeatAt.UTC().Format(time.RFC3339))
	}
	return &st, nil
}

// DryRun has every validator whose filex the range admits run the build for
// its platform. One check per validator; err is for a failure worth retrying
// (a validator not running, a download the network broke, no answer in
// time), never for a build that fails.
func (r *Runner) DryRun(ctx context.Context, req domain.DryRunRequest) ([]domain.DryRunResult, []domain.CheckResult, error) {
	start := r.o.Now().UTC()
	rng, err := filexrange.Parse(req.FilexRange)
	if err != nil {
		end := r.o.Now().UTC()
		return nil, []domain.CheckResult{{Kind: domain.CheckDryRun, Status: domain.CheckFailed, Code: CodeRangeInvalid,
			Message: "the filex range does not parse: " + err.Error(), StartedAt: start, EndedAt: &end}}, nil
	}
	type slot struct {
		res   *domain.DryRunResult
		check domain.CheckResult
		err   error
	}
	slots := make([]slot, len(r.o.Validators))
	var wg sync.WaitGroup
	for i, v := range r.o.Validators {
		wg.Add(1)
		go func(i int, v Validator) {
			defer wg.Done()
			res, check, err := r.one(ctx, v, rng, req)
			slots[i] = slot{res, check, err}
		}(i, v)
	}
	wg.Wait()
	var results []domain.DryRunResult
	var checks []domain.CheckResult
	ran := 0
	for _, s := range slots {
		if s.err != nil {
			return nil, nil, s.err
		}
		checks = append(checks, s.check)
		if s.res != nil {
			results = append(results, *s.res)
			ran++
		}
	}
	if ran == 0 {
		end := r.o.Now().UTC()
		checks = append(checks, domain.CheckResult{Kind: domain.CheckDryRun, Status: domain.CheckFailed, Code: CodeNoValidatorInRange,
			Message:   fmt.Sprintf("the feed's range %q admits none of the store's plugin validators", rng.String()),
			StartedAt: start, EndedAt: &end})
	}
	return results, checks, nil
}

// job is <spool>/in/<id>.json.
type job struct {
	Protocol  int       `json:"protocol"`
	ID        string    `json:"id"`
	SHA256    string    `json:"sha256"`
	Name      string    `json:"name"`
	Version   string    `json:"version"`
	CreatedAt time.Time `json:"created_at"`
}

// Result is <spool>/out/<id>.json, as filex's plugin-validator writes it.
type Result struct {
	Protocol     int             `json:"protocol"`
	ID           string          `json:"id"`
	SHA256       string          `json:"sha256"`
	FilexVersion string          `json:"filex_version"`
	Platform     string          `json:"platform"`
	OK           bool            `json:"ok"`
	Code         string          `json:"code,omitempty"`
	Message      string          `json:"message,omitempty"`
	Describe     *Described      `json:"describe,omitempty"`
	Conformance  json.RawMessage `json:"conformance,omitempty"`
}

// Described is what the build said of itself (filex's describe).
type Described struct {
	Protocol     int             `json:"protocol"`
	Name         string          `json:"name"`
	Version      string          `json:"version"`
	Label        string          `json:"label"`
	Capabilities map[string]bool `json:"capabilities"`
}

// report is the part of filex's conformance report the store counts.
type report struct {
	Verified bool `json:"verified"`
	Results  []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	} `json:"results"`
}

var idRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (r *Runner) one(ctx context.Context, v Validator, rng filexrange.Range, req domain.DryRunRequest) (*domain.DryRunResult, domain.CheckResult, error) {
	check := domain.CheckResult{Kind: domain.CheckDryRun, Validator: v.Label, StartedAt: r.o.Now().UTC()}
	finish := func(status domain.CheckStatus, code, msg string, data any) domain.CheckResult {
		end := r.o.Now().UTC()
		check.Status, check.Code, check.Message, check.EndedAt = status, code, msg, &end
		if data != nil {
			if b, err := json.Marshal(data); err == nil {
				check.Data = b
			}
		}
		return check
	}
	st, err := r.status(v)
	if err != nil {
		return nil, check, err
	}
	host, enforced := filexrange.HostRelease(st.FilexVersion)
	version := strings.TrimPrefix(strings.Fields(st.FilexVersion)[0], "v")
	if enforced {
		version = host.Plain()
	}
	if enforced && !rng.Admits(host) {
		return nil, finish(domain.CheckSkipped, CodeOutOfRange, fmt.Sprintf("filex %s is outside the feed's range %q", version, rng.String()),
			map[string]any{"filex_version": version, "platform": st.Platform}), nil
	}
	res := &domain.DryRunResult{Validator: v.Label, FilexVersion: version, Kind: domain.KindStorage, Permissions: []domain.PermissionRow{}}
	bin, ok := req.Binaries[st.Platform]
	if !ok {
		res.Code, res.Message = CodeNoBuild, "the feed has no build for "+st.Platform+", the platform this validator runs"
		return res, finish(domain.CheckFailed, res.Code, "filex "+version+": "+res.Message, map[string]any{"filex_version": version, "platform": st.Platform}), nil
	}
	select {
	case r.sems[v.Label] <- struct{}{}:
	case <-ctx.Done():
		return nil, check, ctx.Err()
	}
	defer func() { <-r.sems[v.Label] }()

	id, err := newID()
	if err != nil {
		return nil, check, err
	}
	in := filepath.Join(v.Dir, "in")
	binPath := filepath.Join(in, id+".bin")
	jobPath := filepath.Join(in, id+".json")
	defer func() {
		_ = os.Remove(jobPath)
		_ = os.Remove(binPath)
	}()
	sum, size, code, msg, err := r.stage(ctx, binPath, bin.URL, st.Platform)
	if err != nil {
		return nil, check, err
	}
	if code != "" {
		res.Code, res.Message = code, msg
		return res, finish(domain.CheckFailed, code, "filex "+version+": "+msg, map[string]any{"filex_version": version, "platform": st.Platform}), nil
	}
	if !strings.EqualFold(sum, bin.SHA256) {
		res.Code = CodeSHA256Mismatch
		res.Message = fmt.Sprintf("the %s build downloaded for the run is %s, the feed pins %s", st.Platform, sum, strings.ToLower(bin.SHA256))
		return res, finish(domain.CheckFailed, res.Code, "filex "+version+": "+res.Message, map[string]any{"filex_version": version, "platform": st.Platform}), nil
	}
	j := job{Protocol: Protocol, ID: id, SHA256: strings.ToLower(sum), Name: req.Name, Version: req.Version, CreatedAt: r.o.Now().UTC()}
	if err := writeFileAtomic(jobPath, j); err != nil {
		return nil, check, fmt.Errorf("plugin validator %s: the job could not be written: %w", v.Label, err)
	}
	out, err := r.await(ctx, v, id)
	if err != nil {
		return nil, check, err
	}
	run := &domain.StorageRun{Platform: st.Platform, BinarySHA256: strings.ToLower(out.SHA256), Report: out.Conformance}
	res.Storage = run
	res.Raw, _ = json.Marshal(out)
	if out.FilexVersion != "" {
		if h, ok := filexrange.HostRelease(out.FilexVersion); ok {
			res.FilexVersion = h.Plain()
		}
	}
	data := map[string]any{"filex_version": res.FilexVersion, "platform": st.Platform, "binary_sha256": run.BinarySHA256, "bytes": size}
	if out.Describe != nil {
		run.Driver, run.Version, run.Label, run.Capabilities = out.Describe.Name, out.Describe.Version, out.Describe.Label, out.Describe.Capabilities
		data["driver"], data["version"] = run.Driver, run.Version
	}
	if !out.OK {
		res.Code, res.Message = out.Code, out.Message
		if res.Code == "" {
			res.Code = CodeConformanceFailed
		}
		if res.Message == "" {
			res.Message = "the plugin validator refused the build"
		}
		return res, finish(domain.CheckFailed, res.Code, "filex "+res.FilexVersion+": "+res.Message, data), nil
	}
	sumUp, problems := judge(out, run, req)
	run.Summary = sumUp
	if sumUp != nil {
		sumUp.Filex = res.FilexVersion
		data["passed"], data["failed"], data["skipped"] = sumUp.Passed, sumUp.Failed, sumUp.Skipped
	}
	if len(problems) > 0 {
		res.Code, res.Message = problems[0].code, joinProblems(problems)
		data["problems"] = problems
		return res, finish(domain.CheckFailed, res.Code, "filex "+res.FilexVersion+": "+res.Message, data), nil
	}
	res.OK = true
	return res, finish(domain.CheckPassed, "", fmt.Sprintf("filex %s ran the %s build: %d probes passed, %d capabilities not declared",
		res.FilexVersion, st.Platform, sumUp.Passed, sumUp.Skipped), data), nil
}

// stage downloads the build into the spool (the bytes are written under a
// temporary name and renamed into place only once complete). It answers the
// sha256 and size, or a failed check's code and sentence, or a retryable
// error.
func (r *Runner) stage(ctx context.Context, binPath, url, platform string) (string, int64, string, string, error) {
	tmp := binPath + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", 0, "", "", fmt.Errorf("the plugin validator's spool is not writable: %w", err)
	}
	sum, size, ferr := r.o.Fetch(ctx, url, r.o.MaxBinary, f)
	cerr := f.Close()
	if ferr == nil && cerr != nil {
		ferr = cerr
	}
	if ferr != nil {
		_ = os.Remove(tmp)
		switch {
		case safehttp.Refused(ferr), errors.Is(ferr, github.ErrNotFound):
			return "", 0, CodeFetchFailed, "the " + platform + " build could not be downloaded: " + ferr.Error(), nil
		default:
			return "", 0, "", "", fmt.Errorf("the %s build could not be downloaded: %w", platform, ferr)
		}
	}
	if err := os.Rename(tmp, binPath); err != nil {
		_ = os.Remove(tmp)
		return "", 0, "", "", err
	}
	return sum, size, "", "", nil
}

// await waits for the validator's result of job id.
func (r *Runner) await(ctx context.Context, v Validator, id string) (*Result, error) {
	path := filepath.Join(v.Dir, "out", id+".json")
	deadline := r.o.Now().Add(r.o.Timeout)
	t := time.NewTicker(r.o.Poll)
	defer t.Stop()
	for {
		b, err := readCapped(path, maxResultBytes)
		if errors.Is(err, errTooLarge) {
			return nil, fmt.Errorf("plugin validator %s: its result for the job is not one this store reads (%v)", v.Label, err)
		}
		if err == nil {
			var out Result
			if err := json.Unmarshal(b, &out); err != nil || out.Protocol != Protocol || out.ID != id {
				return nil, fmt.Errorf("plugin validator %s: its result for the job is not one this store reads", v.Label)
			}
			return &out, nil
		}
		if r.o.Now().After(deadline) {
			return nil, fmt.Errorf("plugin validator %s did not answer within %s", v.Label, r.o.Timeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

type problem struct {
	code string
	msg  string
}

func (p problem) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"code": p.code, "message": p.msg})
}

func joinProblems(ps []problem) string {
	msgs := make([]string, len(ps))
	for i, p := range ps {
		msgs[i] = p.msg
	}
	return strings.Join(msgs, "; ")
}

// judge holds a validator's passing answer to the store's pins and counts
// its report: the build it ran is the one pinned, it describes itself as the
// feed's version, and its conformance was run - against its own selftest
// area - and verified. A plugin that offers no selftest proves nothing, and
// the store lists only what proved its claims.
func judge(out *Result, run *domain.StorageRun, req domain.DryRunRequest) (*domain.Conformance, []problem) {
	var ps []problem
	add := func(code, f string, args ...any) { ps = append(ps, problem{code, fmt.Sprintf(f, args...)}) }
	if b, ok := req.Binaries[run.Platform]; !ok || !strings.EqualFold(b.SHA256, run.BinarySHA256) {
		add(CodeSHA256Mismatch, "the validator ran a %s build with sha256 %s, not the one the feed pins", run.Platform, run.BinarySHA256)
	}
	if out.Describe == nil {
		add(CodeAnswerIncomplete, "the validator's answer has no describe")
		return nil, ps
	}
	if strings.TrimSpace(out.Describe.Name) != req.Name {
		add(CodeNameMismatch, "the build describes itself as %q, the feed names %q: filex installs a plugin under the name it describes, and checks its updates against the feed's", out.Describe.Name, req.Name)
	}
	if strings.TrimPrefix(strings.TrimSpace(out.Describe.Version), "v") != strings.TrimPrefix(strings.TrimSpace(req.Version), "v") {
		add(CodeVersionMismatch, "the build describes itself as version %q, the feed says %q: filex compares the two to tell a newer release", out.Describe.Version, req.Version)
	}
	var rep report
	if len(out.Conformance) == 0 || json.Unmarshal(out.Conformance, &rep) != nil {
		add(CodeUnverified, "the plugin offers no /v1/selftest, so none of its claims could be probed; the store lists a storage plugin that proves them")
		return nil, ps
	}
	s := &domain.Conformance{Platform: run.Platform, Verified: rep.Verified, Driver: out.Describe.Name, Capabilities: []string{}}
	var failed []string
	for _, p := range rep.Results {
		switch p.Status {
		case "pass":
			s.Passed++
		case "fail":
			s.Failed++
			failed = append(failed, p.Name+": "+p.Detail)
		default:
			s.Skipped++
		}
	}
	for name, on := range out.Describe.Capabilities {
		if on {
			s.Capabilities = append(s.Capabilities, name)
		}
	}
	sort.Strings(s.Capabilities)
	if !rep.Verified || s.Failed > 0 {
		msg := "the plugin fails its own claims"
		if len(failed) > 0 {
			msg += " - " + strings.Join(failed, "; ")
		}
		add(CodeConformanceFailed, "%s", msg)
	}
	if s.Passed == 0 && len(failed) == 0 {
		add(CodeUnverified, "the conformance run probed nothing")
	}
	return s, ps
}

// writeFileAtomic writes v as JSON to a temporary name beside path and
// renames it into place (0644: the validator reads it as another user).
func writeFileAtomic(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".partial"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ValidID reports whether s is a job id the spool uses.
func ValidID(s string) bool { return idRe.MatchString(s) }
