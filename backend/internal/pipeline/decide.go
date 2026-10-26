package pipeline

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/index"
)

// FirstFailure is the first failed check (the order the steps ran in).
func FirstFailure(checks []domain.CheckResult) (domain.CheckResult, bool) {
	for _, c := range checks {
		if c.Status == domain.CheckFailed {
			return c, true
		}
	}
	return domain.CheckResult{}, false
}

// DryRunRequestFor pins a dry run to the resolver's measurements.
func DryRunRequestFor(res *domain.Resolution) domain.DryRunRequest {
	req := domain.DryRunRequest{
		Repo:                 res.RepoOwner + "/" + res.RepoName,
		Ref:                  res.Ref,
		Kind:                 res.Kind,
		FilexRange:           res.FilexRange,
		ExpectManifestSHA256: res.ManifestSHA256,
		ExpectPermissions:    append([]string{}, res.Permissions...),
	}
	if res.Wasm != nil {
		req.ExpectWasmSHA256 = res.Wasm.SHA256
	}
	if res.UI != nil {
		req.ExpectUISHA256 = res.UI.SHA256
	}
	if res.Kind == domain.KindStorage {
		req.Name, req.Version = res.Name, res.Version
		req.Binaries = map[string]domain.Artifact{}
		for plat, a := range res.Binaries {
			req.Binaries[plat] = a
		}
	}
	return req
}

// PermissionRule is filex's rule for reading the permission ids of a dry
// run: Normalize writes ids the way the manifest's are written, IsDerived
// reports an id filex adds itself from the manifest's `ui` or `thumbnails`
// block (in the answer, never in the manifest's `permissions`). The server
// passes resolve.NormalizePermissions and resolve.IsDerivedPermission - one
// rule with the validator's own comparison; it is handed in because
// internal/resolve imports this package. Without IsDerived the permission
// comparison is left to the dry runner (which applies the same rule).
type PermissionRule struct {
	Normalize func([]string) []string
	IsDerived func(string) bool
}

// declared is the answer's permission ids without the derived ones.
func (p PermissionRule) declared(rows []domain.PermissionRow) []string {
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	if p.Normalize != nil {
		ids = p.Normalize(ids)
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !p.IsDerived(id) {
			out = append(out, id)
		}
	}
	return out
}

// EnforcePins checks every validator's answer against the resolver's pins
// once more - the hashes a version is approved with are the ones the
// resolver measured AND every validator reported, so a disagreement never
// reaches review whatever the dry runner decided. Each dry_run row gets the
// validator's answer as its data. A row whose answer disagrees becomes
// failed (manifest_sha256_mismatch, sha256_mismatch, kind_mismatch or
// permissions_mismatch, the validator lane's words).
func EnforcePins(res *domain.Resolution, results []domain.DryRunResult, checks []domain.CheckResult, now time.Time, rule PermissionRule) []domain.CheckResult {
	byLabel := map[string]domain.DryRunResult{}
	for _, r := range results {
		byLabel[r.Validator] = r
	}
	out := make([]domain.CheckResult, 0, len(checks))
	seen := map[string]bool{}
	for _, c := range checks {
		if c.Kind != domain.CheckDryRun {
			out = append(out, c)
			continue
		}
		seen[c.Validator] = true
		r, ok := byLabel[c.Validator]
		if ok {
			c.Data, _ = json.Marshal(r)
		}
		if ok && (c.Status == domain.CheckPassed || c.Status == domain.CheckWarned) {
			if code, msg := mismatch(res, r, rule); code != "" {
				c.Status, c.Code, c.Message = domain.CheckFailed, code, msg
			}
		}
		out = append(out, c)
	}
	// An answer without a row of its own still gets one: nothing a
	// validator said is dropped.
	labels := make([]string, 0, len(byLabel))
	for l := range byLabel {
		if !seen[l] {
			labels = append(labels, l)
		}
	}
	sort.Strings(labels)
	for _, l := range labels {
		r := byLabel[l]
		c := domain.CheckResult{Kind: domain.CheckDryRun, Validator: l, StartedAt: now, EndedAt: &now}
		c.Data, _ = json.Marshal(r)
		switch code, msg := mismatch(res, r, rule); {
		case !r.OK:
			c.Status, c.Code, c.Message = domain.CheckFailed, r.Code, r.Message
		case code != "":
			c.Status, c.Code, c.Message = domain.CheckFailed, code, msg
		default:
			c.Status = domain.CheckPassed
		}
		out = append(out, c)
	}
	return out
}

func mismatch(res *domain.Resolution, r domain.DryRunResult, rule PermissionRule) (string, string) {
	if !r.OK {
		return "", ""
	}
	if res.Kind == domain.KindStorage {
		// A plugin validator ran ONE build: the one for its platform, and it
		// must be the very build the resolver pinned.
		if r.Storage == nil {
			return "answer_incomplete", fmt.Sprintf("the plugin validator %s did not say which build it ran.", r.Validator)
		}
		b, ok := res.Binaries[r.Storage.Platform]
		if !ok || !strings.EqualFold(b.SHA256, r.Storage.BinarySHA256) {
			return "sha256_mismatch", fmt.Sprintf("filex %s ran a %s build with sha256 %s, not the one the resolver pinned.", r.FilexVersion, r.Storage.Platform, r.Storage.BinarySHA256)
		}
		if r.Kind != domain.KindStorage {
			return "kind_mismatch", fmt.Sprintf("filex %s reads this as %s, the resolver as a storage plugin.", r.FilexVersion, r.Kind)
		}
		return "", ""
	}
	if r.Storage != nil {
		return "kind_mismatch", fmt.Sprintf("filex %s ran it as a storage plugin, the resolver read %s.", r.FilexVersion, res.Kind)
	}
	if r.ManifestSHA256 != "" && !strings.EqualFold(r.ManifestSHA256, res.ManifestSHA256) {
		return "manifest_sha256_mismatch", fmt.Sprintf("filex %s measured the manifest as %s, the resolver as %s.", r.FilexVersion, r.ManifestSHA256, res.ManifestSHA256)
	}
	if res.Wasm != nil && r.WasmSHA256 != "" && !strings.EqualFold(r.WasmSHA256, res.Wasm.SHA256) {
		return "sha256_mismatch", fmt.Sprintf("filex %s measured the module as %s, the resolver as %s.", r.FilexVersion, r.WasmSHA256, res.Wasm.SHA256)
	}
	if res.Wasm == nil && r.WasmSHA256 != "" {
		return "sha256_mismatch", fmt.Sprintf("filex %s found a module the resolver did not.", r.FilexVersion)
	}
	if res.UI != nil && r.UISHA256 != "" && !strings.EqualFold(r.UISHA256, res.UI.SHA256) {
		return "sha256_mismatch", fmt.Sprintf("filex %s measured the interface bundle as %s, the resolver as %s.", r.FilexVersion, r.UISHA256, res.UI.SHA256)
	}
	if r.Kind != "" && res.Kind != "" && r.Kind != res.Kind {
		return "kind_mismatch", fmt.Sprintf("filex %s reads this as %s, the resolver as %s.", r.FilexVersion, r.Kind, res.Kind)
	}
	if rule.IsDerived == nil {
		return "", ""
	}
	got := rule.declared(r.Permissions)
	want := res.Permissions
	if rule.Normalize != nil {
		want = rule.Normalize(want)
	}
	if !sameSet(got, want) {
		return "permissions_mismatch", fmt.Sprintf("filex %s lists the declared permissions %v, the manifest %v.", r.FilexVersion, got, want)
	}
	return "", ""
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string{}, a...)
	y := append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// DryRunOutcome decides a dry-run step: failed when a row failed, failed
// (no_validator_in_range) when no row passed, else ready for review.
func DryRunOutcome(checks []domain.CheckResult) (ok bool, code, message string) {
	passed := false
	for _, c := range checks {
		if c.Kind != domain.CheckDryRun {
			continue
		}
		switch c.Status {
		case domain.CheckFailed:
			msg := c.Message
			if c.Validator != "" {
				msg = c.Validator + ": " + msg
			}
			code := c.Code
			if code == "" {
				code = "dry_run_failed"
			}
			return false, code, msg
		case domain.CheckPassed, domain.CheckWarned:
			passed = true
		}
	}
	if !passed {
		return false, "no_validator_in_range", "No validator's filex version is inside the manifest's range."
	}
	return true, "", ""
}

// DryRuns reads the validators' answers back from an attempt's dry_run rows.
func DryRuns(checks []domain.CheckResult) []domain.DryRunResult {
	out := []domain.DryRunResult{}
	for _, c := range checks {
		if c.Kind != domain.CheckDryRun || len(c.Data) == 0 {
			continue
		}
		var r domain.DryRunResult
		if json.Unmarshal(c.Data, &r) == nil && r.Validator != "" {
			out = append(out, r)
		}
	}
	return out
}

// Primary is the answer a version keeps (filex's own words for its
// permissions, its languages): the passing answer of the newest filex.
func Primary(checks []domain.CheckResult) (domain.DryRunResult, bool) {
	var best domain.DryRunResult
	found := false
	for _, c := range checks {
		if c.Kind != domain.CheckDryRun || (c.Status != domain.CheckPassed && c.Status != domain.CheckWarned) {
			continue
		}
		var r domain.DryRunResult
		if json.Unmarshal(c.Data, &r) != nil || !r.OK {
			continue
		}
		if !found || index.CompareVersions(r.FilexVersion, best.FilexVersion) > 0 {
			best, found = r, true
		}
	}
	return best, found
}

// ReadyForApproval reports whether every check of the attempt is done and
// none failed, with at least one dry run passed.
func ReadyForApproval(checks []domain.CheckResult) (bool, string) {
	dry := false
	for _, c := range checks {
		switch c.Status {
		case domain.CheckFailed:
			return false, fmt.Sprintf("The %s check failed.", c.Kind)
		case domain.CheckPending, domain.CheckRunning:
			return false, fmt.Sprintf("The %s check has not finished.", c.Kind)
		}
		if c.Kind == domain.CheckDryRun && (c.Status == domain.CheckPassed || c.Status == domain.CheckWarned) {
			dry = true
		}
	}
	if !dry {
		return false, "No dry run has passed."
	}
	return true, ""
}
