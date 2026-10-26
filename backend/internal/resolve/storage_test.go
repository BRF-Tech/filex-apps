package resolve

import (
	"bytes"
	"context"
	"crypto/x509"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/binary"
	"encoding/json"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/github"
	"github.com/brf-tech/filex-apps/backend/internal/safehttp"
)

// storageResolver is fake's resolver with GitHub's web host (release
// downloads, where a storage plugin's feed is read) pointed at the fake too.
func (f *fake) storageResolver(t *testing.T, lim Limits) *Resolver {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(f.srv.Certificate())
	u, _ := url.Parse(f.srv.URL)
	hc := safehttp.New(safehttp.Config{AllowHosts: []string{u.Host}, RootCAs: pool, Addresses: func(netip.Addr) error { return nil }})
	gh := github.New(github.Options{HTTP: hc, APIURL: f.srv.URL, RawURL: f.srv.URL, WebURL: f.srv.URL})
	clock := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	return New(Options{GitHub: gh, Limits: lim, Now: func() time.Time { return clock }})
}

var (
	amd64Build = elfBuild(elf.ELFCLASS64, elf.EM_X86_64, elf.ET_EXEC, false, "linux/amd64 build of myfs")
	arm64Build = elfBuild(elf.ELFCLASS64, elf.EM_AARCH64, elf.ET_EXEC, false, "linux/arm64 build of myfs")
	winBuild   = peBuild(pe.IMAGE_FILE_MACHINE_AMD64, false, "windows/amd64 build of myfs")
)

// elfBuild is the start of a little-endian ELF of class for machine: its
// header and two program headers (the first PT_INTERP when dynamic, the
// dynamic loader a dynamically linked program names), then tail.
func elfBuild(class elf.Class, m elf.Machine, t elf.Type, dynamic bool, tail string) []byte {
	var b bytes.Buffer
	h := elf.Header64{Type: uint16(t), Machine: uint16(m), Version: uint32(elf.EV_CURRENT), Phoff: 64, Ehsize: 64, Phentsize: 56, Phnum: 2}
	copy(h.Ident[:], elf.ELFMAG)
	h.Ident[elf.EI_CLASS], h.Ident[elf.EI_DATA], h.Ident[elf.EI_VERSION] = byte(class), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)
	_ = binary.Write(&b, binary.LittleEndian, h)
	first := elf.PT_LOAD
	if dynamic {
		first = elf.PT_INTERP
	}
	_ = binary.Write(&b, binary.LittleEndian, elf.Prog64{Type: uint32(first), Flags: uint32(elf.PF_R)})
	_ = binary.Write(&b, binary.LittleEndian, elf.Prog64{Type: uint32(elf.PT_LOAD), Flags: uint32(elf.PF_R | elf.PF_X)})
	b.WriteString(tail)
	return b.Bytes()
}

// peBuild is the start of a 64-bit PE image for machine (a DLL when dll):
// the DOS header pointing at the PE signature, the file header and the
// optional header's magic, then tail.
func peBuild(machine uint16, dll bool, tail string) []byte {
	var b bytes.Buffer
	dos := make([]byte, 0x40)
	copy(dos, "MZ")
	binary.LittleEndian.PutUint32(dos[0x3c:], 0x40)
	b.Write(dos)
	b.WriteString("PE\x00\x00")
	fh := pe.FileHeader{Machine: machine, NumberOfSections: 1, SizeOfOptionalHeader: 240,
		Characteristics: pe.IMAGE_FILE_EXECUTABLE_IMAGE | pe.IMAGE_FILE_LARGE_ADDRESS_AWARE}
	if dll {
		fh.Characteristics |= pe.IMAGE_FILE_DLL
	}
	_ = binary.Write(&b, binary.LittleEndian, fh)
	_ = binary.Write(&b, binary.LittleEndian, uint16(0x20b))
	b.WriteString(tail)
	return b.Bytes()
}

// machoBuild is the start of a thin 64-bit Mach-O of type t for cpu, in
// byte order bo.
func machoBuild(bo binary.ByteOrder, cpu macho.Cpu, t macho.Type) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, bo, macho.FileHeader{Magic: macho.Magic64, Cpu: cpu, Type: t})
	_ = binary.Write(&b, bo, uint32(0))
	return b.Bytes()
}

// universalBuild is the start of a universal (fat) Mach-O with a slice for
// each cpu.
func universalBuild(cpus ...macho.Cpu) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, macho.MagicFat)
	_ = binary.Write(&b, binary.BigEndian, uint32(len(cpus)))
	for i, c := range cpus {
		_ = binary.Write(&b, binary.BigEndian, macho.FatArchHeader{Cpu: c, Offset: uint32(4096 * (i + 1)), Size: 4096, Align: 12})
	}
	return b.Bytes()
}

// storageFixture lays out a storage plugin at BRF-Tech/filex-sign v0.3.0 (the
// recorded repository and tag; what it holds is made up): the release's
// filex-storage.json naming three builds, the builds themselves, and the
// consent file. mutate edits the feed before it is published.
func (f *fake) storageFixture(t *testing.T, mutate func(map[string]any), consentDoc string) []byte {
	t.Helper()
	base := "/BRF-Tech/filex-sign/releases/download/v0.3.0/"
	feed := map[string]any{
		"name": "myfs", "version": "0.3.0", "filex": ">=0.55.0", "notes": "Faster listings.",
		"binaries": map[string]any{
			"linux/amd64":   map[string]string{"url": f.asset(base+"myfs-linux-amd64", amd64Build), "sha256": sum(amd64Build)},
			"linux/arm64":   map[string]string{"url": f.asset(base+"myfs-linux-arm64", arm64Build), "sha256": strings.ToUpper(sum(arm64Build))},
			"windows/amd64": map[string]string{"url": f.asset(base+"myfs-windows-amd64.exe", winBuild), "sha256": sum(winBuild), "signature": "abcd"},
		},
	}
	if mutate != nil {
		mutate(feed)
	}
	fb, _ := json.Marshal(feed)
	f.asset(base+FeedFile, fb)
	if consentDoc == "" {
		consentDoc = `{"publisher":"brf-tech","categories":["storage"],"summary":{"en":"My storage."}}`
	}
	f.put(signRepo, signCommit, ".filex-apps.json", []byte(consentDoc))
	return fb
}

func storageReq() domain.ResolveRequest {
	r := req("user-token")
	r.Kind = domain.SubmitStorage
	return r
}

func TestResolve_AStoragePluginEndToEnd(t *testing.T) {
	f := newFake(t)
	fb := f.storageFixture(t, nil, "")
	res, checks, err := f.storageResolver(t, Limits{}).Resolve(context.Background(), storageReq())
	if err != nil {
		t.Fatal(err)
	}
	want := "repo_access=passed tag=passed manifest=passed consent=passed artifacts=passed media=skipped"
	if got := statuses(checks); got != want {
		t.Fatalf("checks: %s\nwant:   %s\n%s", got, want, last(checks).Message)
	}
	if res.Kind != domain.KindStorage || res.Name != "myfs" || res.Version != "0.3.0" || res.FilexRange != ">=0.55.0" {
		t.Errorf("feed fields %s %s %s %q", res.Kind, res.Name, res.Version, res.FilexRange)
	}
	if res.ManifestSHA256 != sum(fb) || !strings.HasSuffix(res.ManifestURL, "/releases/download/v0.3.0/filex-storage.json") {
		t.Errorf("the feed is the manifest: %s %s", res.ManifestSHA256, res.ManifestURL)
	}
	if len(res.Permissions) != 0 || res.Wasm != nil || res.UI != nil {
		t.Errorf("a storage plugin has no permissions, module or interface: %v %v %v", res.Permissions, res.Wasm, res.UI)
	}
	if len(res.Binaries) != 3 {
		t.Fatalf("binaries %v", res.Binaries)
	}
	for plat, b := range map[string][]byte{"linux/amd64": amd64Build, "linux/arm64": arm64Build, "windows/amd64": winBuild} {
		got := res.Binaries[plat]
		if got.SHA256 != sum(b) || got.Size != int64(len(b)) {
			t.Errorf("%s: %+v (the bytes downloaded, lower-case)", plat, got)
		}
	}
	if res.FeedNotes != "Faster listings." {
		t.Errorf("notes %q", res.FeedNotes)
	}
}

func TestResolve_AStorageBuildThatIsNotWhatTheFeedPinsFails(t *testing.T) {
	f := newFake(t)
	f.storageFixture(t, func(feed map[string]any) {
		bins := feed["binaries"].(map[string]any)
		arm := bins["linux/arm64"].(map[string]string)
		arm["sha256"] = sum([]byte("another build"))
	}, "")
	res, checks, err := f.storageResolver(t, Limits{}).Resolve(context.Background(), storageReq())
	if err != nil {
		t.Fatal(err)
	}
	c := last(checks)
	if c.Kind != domain.CheckArtifacts || c.Status != domain.CheckFailed || c.Code != CodeSHA256Mismatch {
		t.Fatalf("checks %s: %s", statuses(checks), c.Message)
	}
	if !strings.Contains(c.Message, "linux/arm64 build") || !strings.Contains(c.Message, "the feed pins") {
		t.Errorf("message %q", c.Message)
	}
	if res.Binaries != nil {
		t.Errorf("no build is kept from a failed check: %v", res.Binaries)
	}
}

func TestResolve_AStorageBuildMustBeItsPlatformsExecutable(t *testing.T) {
	// Self-contained builds only: filex starts a storage plugin as a program
	// of its own, on images that carry no interpreter, so a script (or an
	// executable of another platform's format) is refused before review.
	base := "/BRF-Tech/filex-sign/releases/download/v0.3.0/"
	script := []byte("#!/bin/sh\nexec python3 \"$(dirname \"$0\")/myfs.py\" \"$@\"\n")
	cases := []struct {
		name, plat, want string
		build            []byte
	}{
		{"a shell script as the arm64 build", "linux/arm64", "is a script (it starts with #!)", script},
		{"a Linux executable named as the windows build", "windows/amd64", "not a Windows executable (PE)", amd64Build},
		{"a Python file as the darwin build", "darwin/arm64", "not a macOS executable (Mach-O)", []byte("import sys\n")},
		{"an amd64 Linux executable as the arm64 build", "linux/arm64", "a Linux executable for amd64, not for arm64", amd64Build},
		{"a dynamically linked Linux executable", "linux/amd64", "dynamically linked (it names a program interpreter, PT_INTERP)",
			elfBuild(elf.ELFCLASS64, elf.EM_X86_64, elf.ET_EXEC, true, "")},
		{"a 32-bit Linux executable", "linux/amd64", "32-bit or big-endian ELF", elfBuild(elf.ELFCLASS32, elf.EM_386, elf.ET_EXEC, false, "")},
		{"a Linux object file", "linux/amd64", "an ELF of type ET_REL, not a program", elfBuild(elf.ELFCLASS64, elf.EM_X86_64, elf.ET_REL, false, "")},
		{"an arm64 Windows executable as the amd64 build", "windows/amd64", "a Windows executable for arm64, not for amd64",
			peBuild(pe.IMAGE_FILE_MACHINE_ARM64, false, "")},
		{"a Windows library", "windows/amd64", "a Windows library (DLL) or object", peBuild(pe.IMAGE_FILE_MACHINE_AMD64, true, "")},
		{"a Java class file as the darwin build", "darwin/arm64", "not a macOS executable (Mach-O)",
			[]byte("\xca\xfe\xba\xbe\x00\x00\x00\x34\x00\x1d\x0a\x00\x06\x00\x0f\x09\x00\x10\x00\x11")},
		{"a universal binary without the arm64 slice", "darwin/arm64", "a universal macOS binary without a slice for arm64",
			universalBuild(macho.CpuAmd64)},
		{"a macOS library", "darwin/arm64", "a Mach-O Dylib (a library, a bundle or an object), not a program",
			machoBuild(binary.LittleEndian, macho.CpuArm64, macho.TypeDylib)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t)
			f.storageFixture(t, func(feed map[string]any) {
				feed["binaries"].(map[string]any)[tc.plat] = map[string]string{
					"url": f.asset(base+"myfs-bad", tc.build), "sha256": sum(tc.build)}
			}, "")
			res, checks, err := f.storageResolver(t, Limits{}).Resolve(context.Background(), storageReq())
			if err != nil {
				t.Fatal(err)
			}
			c := last(checks)
			if c.Kind != domain.CheckArtifacts || c.Status != domain.CheckFailed || c.Code != CodeBuildNotNative {
				t.Fatalf("checks %s: %s %s", statuses(checks), c.Code, c.Message)
			}
			if !strings.Contains(c.Message, tc.plat+" build") || !strings.Contains(c.Message, tc.want) {
				t.Errorf("message %q", c.Message)
			}
			if res.Binaries != nil {
				t.Errorf("no build is kept from a failed check: %v", res.Binaries)
			}
		})
	}
}

func TestResolve_AStorageBuildOfEachFormatPasses(t *testing.T) {
	// The optional platforms' builds pass on their own formats: PE for
	// windows, Mach-O (thin, either byte order, or universal) for darwin.
	base := "/BRF-Tech/filex-sign/releases/download/v0.3.0/"
	machO := machoBuild(binary.LittleEndian, macho.CpuArm64, macho.TypeExec)
	universal := universalBuild(macho.CpuArm64, macho.CpuAmd64)
	f := newFake(t)
	f.storageFixture(t, func(feed map[string]any) {
		bins := feed["binaries"].(map[string]any)
		bins["darwin/arm64"] = map[string]string{"url": f.asset(base+"myfs-darwin-arm64", machO), "sha256": sum(machO)}
		bins["darwin/amd64"] = map[string]string{"url": f.asset(base+"myfs-darwin-amd64", universal), "sha256": sum(universal)}
	}, "")
	res, checks, err := f.storageResolver(t, Limits{}).Resolve(context.Background(), storageReq())
	if err != nil {
		t.Fatal(err)
	}
	if c := last(checks); c.Kind != domain.CheckMedia {
		t.Fatalf("checks %s: %s %s", statuses(checks), c.Code, c.Message)
	}
	for _, plat := range []string{"linux/amd64", "linux/arm64", "windows/amd64", "darwin/amd64", "darwin/arm64"} {
		if _, ok := res.Binaries[plat]; !ok {
			t.Errorf("%s build not kept: %v", plat, res.Binaries)
		}
	}
}

func TestNativeProblem(t *testing.T) {
	// Each format's own headers, its platform's architecture, and on Linux
	// no program interpreter: what the store reads of a build's first bytes.
	dynamic := elfBuild(elf.ELFCLASS64, elf.EM_AARCH64, elf.ET_DYN, true, "")
	pie := elfBuild(elf.ELFCLASS64, elf.EM_AARCH64, elf.ET_DYN, false, "")
	for _, tc := range []struct {
		plat string
		head []byte
		ok   bool
	}{
		{"linux/amd64", amd64Build, true},
		{"linux/arm64", arm64Build, true},
		{"linux/arm64", pie, true},
		{"linux/arm64", dynamic, false},
		{"linux/arm64", amd64Build, false},
		{"linux/amd64", []byte("\x7fELF\x02\x01\x01\x00"), false},
		{"linux/amd64", []byte("#!/usr/bin/env node\n"), false},
		{"linux/amd64", winBuild, false},
		{"windows/amd64", winBuild, true},
		{"windows/arm64", peBuild(pe.IMAGE_FILE_MACHINE_ARM64, false, ""), true},
		{"windows/arm64", winBuild, false},
		{"windows/amd64", []byte("MZ\x90\x00"), false},
		{"windows/amd64", amd64Build, false},
		{"darwin/arm64", machoBuild(binary.LittleEndian, macho.CpuArm64, macho.TypeExec), true},
		{"darwin/amd64", machoBuild(binary.BigEndian, macho.CpuAmd64, macho.TypeExec), true},
		{"darwin/amd64", machoBuild(binary.LittleEndian, macho.CpuArm64, macho.TypeExec), false},
		{"darwin/arm64", universalBuild(macho.CpuAmd64, macho.CpuArm64), true},
		{"darwin/arm64", universalBuild(macho.CpuAmd64), false},
		{"darwin/arm64", []byte{0xce, 0xfa, 0xed, 0xfe, 0x0c, 0x00, 0x00, 0x00}, false},
		{"darwin/arm64", []byte{0xca, 0xfe}, false},
		{"darwin/arm64", []byte("PK\x03\x04"), false},
		{"plan9/amd64", amd64Build, false},
	} {
		if why := nativeProblem(tc.plat, tc.head); (why == "") != tc.ok {
			t.Errorf("nativeProblem(%s, % x...) = %q, want ok=%v", tc.plat, tc.head[:min(len(tc.head), 8)], why, tc.ok)
		}
	}
}

func TestHeadWriter_KeepsOnlyTheFirstBytes(t *testing.T) {
	h := &headWriter{max: 8}
	for _, p := range [][]byte{[]byte("\x7fEL"), []byte("F\x02\x01\x01\x00 and the rest"), []byte("more")} {
		if n, err := h.Write(p); n != len(p) || err != nil {
			t.Fatalf("Write(%q) = %d, %v: a short write would stop the download", p, n, err)
		}
	}
	if string(h.head) != "\x7fELF\x02\x01\x01\x00" {
		t.Fatalf("head %q", h.head)
	}
}

func TestResolve_AStorageFeedWithoutAnArm64BuildFails(t *testing.T) {
	f := newFake(t)
	f.storageFixture(t, func(feed map[string]any) {
		delete(feed["binaries"].(map[string]any), "linux/arm64")
	}, "")
	_, checks, err := f.storageResolver(t, Limits{}).Resolve(context.Background(), storageReq())
	if err != nil {
		t.Fatal(err)
	}
	c := last(checks)
	if c.Kind != domain.CheckManifest || c.Code != CodePlatformMissing || !strings.Contains(c.Message, "linux/arm64") {
		t.Fatalf("checks %s: %s", statuses(checks), c.Message)
	}
}

func TestResolve_AStorageReleaseWithoutAFeedFails(t *testing.T) {
	f := newFake(t)
	f.put(signRepo, signCommit, ".filex-apps.json", []byte(`{"publisher":"brf-tech"}`))
	_, checks, err := f.storageResolver(t, Limits{}).Resolve(context.Background(), storageReq())
	if err != nil {
		t.Fatal(err)
	}
	c := last(checks)
	if c.Kind != domain.CheckManifest || c.Code != CodeManifestMissing || !strings.Contains(c.Message, FeedFile) {
		t.Fatalf("checks %s: %s", statuses(checks), c.Message)
	}
}

func TestResolve_AStoragePluginsListingDeclaresNoConversions(t *testing.T) {
	f := newFake(t)
	f.storageFixture(t, nil, `{"publisher":"brf-tech","conversions":[{"from":["docx"],"to":["pdf"]}]}`)
	_, checks, err := f.storageResolver(t, Limits{}).Resolve(context.Background(), storageReq())
	if err != nil {
		t.Fatal(err)
	}
	c := last(checks)
	if c.Kind != domain.CheckConsent || c.Code != CodeConsentInvalid || !strings.Contains(c.Message, "converts no files") {
		t.Fatalf("checks %s: %s", statuses(checks), c.Message)
	}
}

func TestResolve_AnAppSubmissionNeverReadsAFeed(t *testing.T) {
	// The same repository at the same tag submitted as an app: the manifest
	// is filex-app.json, whatever the release carries.
	f := newFake(t)
	f.storageFixture(t, nil, "")
	_, checks, err := f.storageResolver(t, Limits{}).Resolve(context.Background(), req("user-token"))
	if err != nil {
		t.Fatal(err)
	}
	c := last(checks)
	if c.Kind != domain.CheckManifest || c.Code != CodeManifestMissing || !strings.Contains(c.Message, ManifestFile) {
		t.Fatalf("checks %s: %s", statuses(checks), c.Message)
	}
}

func TestParseFeed(t *testing.T) {
	good := func() map[string]any {
		return map[string]any{
			"name": "myfs", "version": "1.0.0",
			"binaries": map[string]any{
				"linux/amd64": map[string]string{"url": "https://github.com/a/b/releases/download/v1/x", "sha256": strings.Repeat("a", 64)},
				"linux/arm64": map[string]string{"url": "https://github.com/a/b/releases/download/v1/y", "sha256": strings.Repeat("b", 64)},
			},
		}
	}
	cases := []struct {
		name   string
		mutate func(map[string]any)
		code   string
	}{
		{"valid", nil, ""},
		{"a name filex refuses", func(m map[string]any) { m["name"] = "My FS" }, CodeManifestBad},
		{"no version", func(m map[string]any) { m["version"] = " " }, CodeManifestBad},
		{"a range that does not parse", func(m map[string]any) { m["filex"] = ">=zero" }, CodeManifestBad},
		{"a platform nothing chooses", func(m map[string]any) {
			m["binaries"].(map[string]any)["plan9/amd64"] = map[string]string{"url": "https://github.com/a/b/z", "sha256": strings.Repeat("c", 64)}
		}, CodePlatformUnknown},
		{"no amd64 build", func(m map[string]any) { delete(m["binaries"].(map[string]any), "linux/amd64") }, CodePlatformMissing},
		{"a plain http address", func(m map[string]any) {
			m["binaries"].(map[string]any)["linux/amd64"] = map[string]string{"url": "http://github.com/a/b/x", "sha256": strings.Repeat("a", 64)}
		}, CodeManifestBad},
		{"a build without its hash", func(m map[string]any) {
			m["binaries"].(map[string]any)["linux/arm64"] = map[string]string{"url": "https://github.com/a/b/y", "sha256": "abc"}
		}, CodeSHA256Required},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := good()
			if tc.mutate != nil {
				tc.mutate(m)
			}
			b, _ := json.Marshal(m)
			p, ferr := parseFeed(b)
			switch {
			case tc.code == "" && ferr != nil:
				t.Fatalf("refused: %v", ferr)
			case tc.code == "" && strings.Join(p.platforms(), ",") != "linux/amd64,linux/arm64":
				t.Fatalf("platforms %v", p.platforms())
			case tc.code != "" && (ferr == nil || ferr.code != tc.code):
				t.Fatalf("want %s, got %v", tc.code, ferr)
			}
		})
	}
}

func TestClipNotes_KeepsWhatFilexKeeps(t *testing.T) {
	long := strings.Repeat("ğ", maxFeedNotes) // two bytes a letter
	got := clipNotes(long)
	if !strings.HasSuffix(got, "…") || len(got) > maxFeedNotes+len("…") {
		t.Fatalf("clipped to %d bytes", len(got))
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatal("cut inside a character")
	}
}
