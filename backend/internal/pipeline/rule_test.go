package pipeline_test

import (
	"strings"
	"testing"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/pipeline"
	"github.com/brf-tech/filex-apps/backend/internal/resolve"
)

// The server's rule: the validator lane's, not a copy.
var filexRule = pipeline.PermissionRule{Normalize: resolve.NormalizePermissions, IsDerived: resolve.IsDerivedPermission}

func TestPinsLeaveTheInterfacesDerivedPermissionsToFilex(t *testing.T) {
	now := time.Now()
	res := &domain.Resolution{Kind: domain.KindApp, ManifestSHA256: strings.Repeat("a", 64), Permissions: []string{"files:read", "files:write"},
		UI: &domain.Artifact{SHA256: strings.Repeat("c", 64)}}
	row := func(ids ...string) []domain.PermissionRow {
		out := make([]domain.PermissionRow, 0, len(ids))
		for _, id := range ids {
			out = append(out, domain.PermissionRow{ID: id, Label: id})
		}
		return out
	}
	check := []domain.CheckResult{{Kind: domain.CheckDryRun, Validator: "latest", Status: domain.CheckPassed}}
	answer := domain.DryRunResult{Validator: "latest", FilexVersion: "0.50.0", OK: true, Kind: domain.KindApp, UISHA256: strings.Repeat("c", 64)}

	// filex-drawio's measured answer: the two declared and five derived.
	answer.Permissions = row("files:read", "files:write", "ui", "ui:package-fetch", "ui-viewer:.drawio", "ui-viewer:.dio", "ui-new:.drawio")
	if out := pipeline.EnforcePins(res, []domain.DryRunResult{answer}, check, now, filexRule); out[0].Status != domain.CheckPassed {
		t.Fatalf("an interface's derived permissions failed the check: %+v", out[0])
	}
	// The office editor app on a filex 0.55 validator: its editor opens a page
	// of its own package in a frame and loads the document from a blob:
	// address, and filex prints its PDFs - three more permissions filex
	// derives from the ui block.
	answer.Permissions = row("files:read", "files:write", "ui", "ui:wasm-eval", "ui:package-fetch", "ui:download", "ui:frame-package", "ui:connect-blob", "ui:print", "ui-viewer:.docx", "ui-new:.docx")
	if out := pipeline.EnforcePins(res, []domain.DryRunResult{answer}, check, now, filexRule); out[0].Status != domain.CheckPassed {
		t.Fatalf("filex 0.55's derived permissions failed the check: %+v", out[0])
	}
	// Its 0.2.0 on a filex 0.56 validator: it edits documents of encrypted
	// folders too (`encrypted_folders`), which filex derives as
	// files:e2e-plaintext - not an `ui:` id, still never declared.
	answer.FilexVersion = "0.56.0"
	answer.Permissions = row("files:read", "files:write", "ui", "ui:wasm-eval", "ui:package-fetch", "ui:download", "ui:frame-package", "ui:connect-blob", "ui:print", "ui-viewer:.docx", "ui-new:.docx", "files:e2e-plaintext")
	if out := pipeline.EnforcePins(res, []domain.DryRunResult{answer}, check, now, filexRule); out[0].Status != domain.CheckPassed {
		t.Fatalf("filex 0.56's derived files:e2e-plaintext failed the check: %+v", out[0])
	}
	// Its 0.4.0 on a filex 0.57 validator: it brings the office engine filex
	// runs on the server (`engines.office`), which filex derives as
	// office-engine - never declared either. Red before: the pin check read
	// it as a permission the manifest does not declare.
	answer.FilexVersion = "0.57.0"
	answer.Permissions = row("files:read", "files:write", "ui", "ui:wasm-eval", "ui:package-fetch", "ui-viewer:.docx", "files:e2e-plaintext", "files:co-edit", "office-engine")
	if out := pipeline.EnforcePins(res, []domain.DryRunResult{answer}, check, now, filexRule); out[0].Status != domain.CheckPassed {
		t.Fatalf("filex 0.57's derived office-engine failed the check: %+v", out[0])
	}
	// A permission the manifest does not declare still fails...
	answer.Permissions = row("files:read", "files:write", "net:fetch", "ui")
	if out := pipeline.EnforcePins(res, []domain.DryRunResult{answer}, check, now, filexRule); out[0].Status != domain.CheckFailed || out[0].Code != "permissions_mismatch" {
		t.Fatalf("an undeclared permission passed: %+v", out[0])
	}
	// ...and so does a declared one filex did not list.
	answer.Permissions = row("files:read", "ui", "ui-new:.drawio")
	if out := pipeline.EnforcePins(res, []domain.DryRunResult{answer}, check, now, filexRule); out[0].Status != domain.CheckFailed || out[0].Code != "permissions_mismatch" {
		t.Fatalf("a missing declared permission passed: %+v", out[0])
	}
}

// The office editor's 0.3.0 on a filex 0.56 validator: it edits a file
// together with other people (`co_edit`), which filex derives as
// files:co-edit - not an `ui:` id, still never declared, so the pin check
// leaves it to filex. Red before: the store read it as undeclared.
func TestPinsLeaveTheCoEditGrantToFilex(t *testing.T) {
	now := time.Now()
	res := &domain.Resolution{Kind: domain.KindApp, ManifestSHA256: strings.Repeat("a", 64), Permissions: []string{"files:read", "files:write"},
		UI: &domain.Artifact{SHA256: strings.Repeat("c", 64)}}
	check := []domain.CheckResult{{Kind: domain.CheckDryRun, Validator: "latest", Status: domain.CheckPassed}}
	answer := domain.DryRunResult{Validator: "latest", FilexVersion: "0.56.0", OK: true, Kind: domain.KindApp, UISHA256: strings.Repeat("c", 64)}
	for _, id := range []string{"files:read", "files:write", "ui", "ui:wasm-eval", "ui:package-fetch", "ui:download", "ui:frame-package", "ui:connect-blob", "ui:print", "ui-viewer:.docx", "ui-new:.docx", "files:co-edit"} {
		answer.Permissions = append(answer.Permissions, domain.PermissionRow{ID: id, Label: id})
	}
	if out := pipeline.EnforcePins(res, []domain.DryRunResult{answer}, check, now, filexRule); out[0].Status != domain.CheckPassed {
		t.Fatalf("filex 0.56's derived files:co-edit failed the check: %+v", out[0])
	}
}
