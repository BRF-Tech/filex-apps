package pipeline

import (
	"bytes"
	"crypto/ed25519"
	"strings"
	"testing"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/signing"
)

// storageRes is a resolved storage plugin with two pinned builds.
func storageRes() *domain.Resolution {
	r := res("myfs", "1.3.0")
	r.Kind = domain.KindStorage
	r.Permissions = []string{}
	r.Binaries = map[string]domain.Artifact{
		"linux/amd64": {URL: "https://github.com/a/b/x", SHA256: strings.Repeat("1", 64), Size: 10},
		"linux/arm64": {URL: "https://github.com/a/b/y", SHA256: strings.Repeat("2", 64), Size: 11},
	}
	return r
}

func storageAnswer() domain.DryRunResult {
	return domain.DryRunResult{Validator: "latest", FilexVersion: "0.55.0", OK: true, Kind: domain.KindStorage,
		Permissions: []domain.PermissionRow{},
		Storage: &domain.StorageRun{Platform: "linux/amd64", BinarySHA256: strings.Repeat("1", 64), Driver: "myfs", Version: "1.3.0",
			Summary: &domain.Conformance{Platform: "linux/amd64", Filex: "0.55.0", Verified: true, Passed: 9, Skipped: 7, Capabilities: []string{"write"}}}}
}

func TestDryRunRequestFor_AStoragePluginCarriesItsBuilds(t *testing.T) {
	req := DryRunRequestFor(storageRes())
	if req.Kind != domain.KindStorage || req.Name != "myfs" || req.Version != "1.3.0" || len(req.Binaries) != 2 {
		t.Fatalf("request %+v", req)
	}
	if app := DryRunRequestFor(res("hello", "1.0.0")); app.Binaries != nil || app.Name != "" {
		t.Fatalf("an app's request names no builds: %+v", app)
	}
}

func TestEnforcePins_TheBuildAValidatorRanIsThePinnedOne(t *testing.T) {
	now := time.Now()
	r := storageRes()
	out := EnforcePins(r, []domain.DryRunResult{storageAnswer()}, []domain.CheckResult{dryRow("latest", domain.CheckPassed)}, now, strict)
	if out[0].Status != domain.CheckPassed {
		t.Fatalf("matching answer failed: %+v", out[0])
	}
	for name, mut := range map[string]func(d *domain.DryRunResult){
		"another build":                  func(d *domain.DryRunResult) { d.Storage.BinarySHA256 = strings.Repeat("9", 64) },
		"a platform that was not pinned": func(d *domain.DryRunResult) { d.Storage.Platform = "darwin/arm64" },
		"no run reported":                func(d *domain.DryRunResult) { d.Storage = nil },
		"read as an app":                 func(d *domain.DryRunResult) { d.Kind = domain.KindApp },
	} {
		d := storageAnswer()
		mut(&d)
		out := EnforcePins(r, []domain.DryRunResult{d}, []domain.CheckResult{dryRow("latest", domain.CheckPassed)}, now, strict)
		if out[0].Status != domain.CheckFailed {
			t.Errorf("%s reached review: %+v", name, out[0])
		}
	}
	// An app's dry run that claims to have run a storage build is refused too.
	app := res("hello", "1.0.0")
	d := storageAnswer()
	d.Kind = domain.KindApp
	if out := EnforcePins(app, []domain.DryRunResult{d}, []domain.CheckResult{dryRow("latest", domain.CheckPassed)}, now, strict); out[0].Code != "kind_mismatch" {
		t.Fatalf("an app answered with a storage run: %+v", out[0])
	}
}

func TestPrimary_KeepsTheStorageRun(t *testing.T) {
	now := time.Now()
	out := EnforcePins(storageRes(), []domain.DryRunResult{storageAnswer()}, []domain.CheckResult{dryRow("latest", domain.CheckPassed)}, now, strict)
	p, ok := Primary(out)
	if !ok || p.Storage == nil || p.Storage.Summary == nil || p.Storage.Summary.Passed != 9 {
		t.Fatalf("primary %+v", p)
	}
}

func TestSignBuilds_EveryPlatformIsSigned(t *testing.T) {
	// The windows and darwin builds are optional, but a listed one is signed
	// like the linux pair: a filex on any platform that requires signed
	// plugins installs only what the store vouched for.
	key, err := signing.NewKeyFromSeed("artifact-test", bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	pub, err := signing.ParsePublicKey(key.PublicHex())
	if err != nil {
		t.Fatal(err)
	}
	bins := storageRes().Binaries
	bins["windows/amd64"] = domain.Artifact{URL: "https://github.com/a/b/w.exe", SHA256: strings.Repeat("A", 64), Size: 12}
	bins["darwin/arm64"] = domain.Artifact{URL: "https://github.com/a/b/m", SHA256: strings.Repeat("4", 64), Size: 13}
	out := signBuilds("myfs", "1.3.0", bins, key)
	if len(out) != len(bins) {
		t.Fatalf("signed %d of %d builds", len(out), len(bins))
	}
	for plat, b := range out {
		if b.SHA256 != strings.ToLower(bins[plat].SHA256) || b.URL != bins[plat].URL || b.Size != bins[plat].Size {
			t.Errorf("%s kept as %+v", plat, b)
		}
		if err := signing.VerifyStorageBuild([]ed25519.PublicKey{pub}, "myfs", "1.3.0", plat, b.SHA256, b.Sig); err != nil {
			t.Errorf("%s: %v", plat, err)
		}
	}
}

// A build's signature names the plugin, the version and the platform it was
// reviewed as (sec055 S5): it does not verify as the sha256 alone (what a
// filex took as "signed" on any install path), nor as another platform's,
// another version's or another plugin's build.
func TestSignBuilds_TheSignatureNamesTheBuild(t *testing.T) {
	key, err := signing.NewKeyFromSeed("artifact-test", bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	pub, err := signing.ParsePublicKey(key.PublicHex())
	if err != nil {
		t.Fatal(err)
	}
	keys := []ed25519.PublicKey{pub}
	out := signBuilds("myfs", "1.3.0", storageRes().Binaries, key)
	b := out["linux/amd64"]
	if err := signing.Verify(keys, b.SHA256, b.Sig); err == nil {
		t.Fatal("the build's signature verifies over its sha256 alone: any install path would take it as this build")
	}
	for _, c := range []struct{ name, version, platform string }{
		{"myfs", "1.3.0", "linux/arm64"},
		{"myfs", "1.2.0", "linux/amd64"},
		{"otherfs", "1.3.0", "linux/amd64"},
	} {
		if err := signing.VerifyStorageBuild(keys, c.name, c.version, c.platform, b.SHA256, b.Sig); err == nil {
			t.Errorf("the linux/amd64 build of myfs 1.3.0 verifies as %s %s on %s", c.name, c.version, c.platform)
		}
	}
	if err := signing.VerifyStorageBuild(keys, "myfs", "v1.3.0", "linux/amd64", b.SHA256, b.Sig); err != nil {
		t.Errorf("a leading v is not part of the signed version: %v", err)
	}
}
