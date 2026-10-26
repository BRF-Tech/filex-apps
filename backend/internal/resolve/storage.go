package resolve

// A storage plugin (docs/ARCHITECTURE.md "Storage plugins"): filex's other
// plugin class, a native program that speaks filex's plugin protocol. It has
// no filex-app.json; what it is and where its builds are is the
// filex-storage.json feed attached to the release - the very file filex reads
// when an administrator installs a plugin "from its source". So the steps of
// an app become, for a storage plugin:
//
//   - manifest: the feed of the submitted tag's release
//     (https://github.com/<owner>/<name>/releases/download/<tag>/filex-storage.json),
//     its sha256 recorded as the manifest's; a name, a version, a filex range
//     that parses, and a build pinned by sha256 for every platform it names -
//     linux/amd64 and linux/arm64 at least (domain.RequiredPlatforms), others
//     only from domain.StoragePlatforms;
//   - consent: the repository's .filex-apps.json, as for an app (conversions
//     refused: a storage plugin converts nothing);
//   - artifacts: EVERY build downloaded through the guarded client and held
//     to the sha256 the feed names (a feed is not a promise about bytes),
//     and each one an executable of its own platform's format - ELF, PE,
//     Mach-O - never a script that needs an interpreter: filex runs a
//     storage plugin as a program of its own, on servers that may have none;
//   - media: as for an app.
//
// The conformance run - the build actually started and its claims probed -
// is the dry-run step's, on the store's plugin validators
// (internal/plugincheck), never here: the resolver downloads, it runs nothing.

import (
	"bytes"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/filexrange"
	"github.com/brf-tech/filex-apps/backend/internal/github"
	"github.com/brf-tech/filex-apps/backend/internal/safehttp"
)

// FeedFile is the name a storage plugin's release attaches its feed under
// (filex's plugin.FeedFileName).
const FeedFile = "filex-storage.json"

// Storage plugin check codes.
const (
	// CodePlatformMissing: the feed has no build for a platform the store
	// requires (domain.RequiredPlatforms).
	CodePlatformMissing = "platform_missing"
	// CodePlatformUnknown: the feed names a build for a platform filex does
	// not choose builds by.
	CodePlatformUnknown = "platform_unknown"
	// CodeBuildNotNative: a build is not an executable of its platform's own
	// format (ELF for linux, PE for windows, Mach-O for darwin) - a script
	// that needs an interpreter, an archive, anything filex cannot start as
	// a program by itself.
	CodeBuildNotNative = "build_not_native"
)

// nativeFormat names the executable format of each OS a feed may name a
// build for, as the refusal says it.
var nativeFormat = map[string]string{
	"linux":   "Linux executable (ELF)",
	"windows": "Windows executable (PE)",
	"darwin":  "macOS executable (Mach-O)",
}

// headMax is how much of a build's start the store keeps while the guarded
// client hashes it: what the format check reads - an ELF's header and
// program headers, a PE's file and optional header, a Mach-O's header or a
// universal binary's slice table - sits in the first bytes. The rest of a
// file (section and symbol tables, which debug/elf, debug/pe and debug/macho
// read eagerly when they open one) would cost the whole build in memory or
// on disk, up to FAPPS_MAX_BINARY_MB per platform, for nothing this check
// asks; so the headers are read here with those packages' own types and
// constants.
const headMax = 64 << 10

// The machine each format names for a feed platform's architecture.
var (
	elfMachine   = map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}
	peMachine    = map[string]uint16{"amd64": pe.IMAGE_FILE_MACHINE_AMD64, "arm64": pe.IMAGE_FILE_MACHINE_ARM64}
	machoMachine = map[string]macho.Cpu{"amd64": macho.CpuAmd64, "arm64": macho.CpuArm64}
)

// pe32Plus is the optional header's magic of a 64-bit image (PE32+).
const pe32Plus = 0x20b

// headWriter keeps the first max bytes written to it and discards the rest:
// a build's headers, read while the guarded client hashes the build.
type headWriter struct {
	head []byte
	max  int
}

func (h *headWriter) Write(p []byte) (int, error) {
	if room := h.max - len(h.head); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		h.head = append(h.head, p[:room]...)
	}
	return len(p), nil
}

// nativeProblem answers why head - a build's first bytes - is not a program
// filex can start by itself on plat ("linux/arm64"), or "" when it is: its
// platform's own format, for its platform's architecture, and on Linux
// statically linked.
//
// ⚠ A dynamically linked ELF (one that names a program interpreter,
// PT_INTERP) is REFUSED, not warned about: it starts only where the loader and
// the C library it was linked against are installed, which nothing promises of
// a filex host - the same reason a #! script is refused. A store that lists a
// build stands behind it starting; a warning would list builds that fail on
// the hosts that lack them, and the store's validator (one platform, one
// image) would not tell. Windows and macOS programs link their system's
// libraries by design, so only Linux builds are held to it.
func nativeProblem(plat string, head []byte) string {
	goos, arch, _ := strings.Cut(plat, "/")
	if bytes.HasPrefix(head, []byte("#!")) {
		return fmt.Sprintf("the %s build is a script (it starts with #!), not a %s: the store lists self-contained builds, never a script that needs an interpreter",
			plat, nativeFormat[goos])
	}
	notFormat := fmt.Sprintf("the %s build is not a %s: the store lists self-contained builds, never a script or an archive that needs an interpreter or an unpacker",
		plat, nativeFormat[goos])
	switch goos {
	case "linux":
		return elfProblem(plat, arch, head, notFormat)
	case "windows":
		return peProblem(plat, arch, head, notFormat)
	case "darwin":
		return machoProblem(plat, arch, head, notFormat)
	}
	return notFormat
}

func elfProblem(plat, arch string, head []byte, notFormat string) string {
	var h elf.Header64
	if !bytes.HasPrefix(head, []byte(elf.ELFMAG)) || binary.Read(bytes.NewReader(head), binary.LittleEndian, &h) != nil {
		return notFormat
	}
	if elf.Class(h.Ident[elf.EI_CLASS]) != elf.ELFCLASS64 || elf.Data(h.Ident[elf.EI_DATA]) != elf.ELFDATA2LSB {
		return fmt.Sprintf("the %s build is a 32-bit or big-endian ELF, not a 64-bit %s program", plat, arch)
	}
	if m := elf.Machine(h.Machine); m != elfMachine[arch] {
		return fmt.Sprintf("the %s build is a Linux executable for %s, not for %s: each platform's build runs on that platform", plat, elfArch(m), arch)
	}
	if t := elf.Type(h.Type); t != elf.ET_EXEC && t != elf.ET_DYN {
		return fmt.Sprintf("the %s build is an ELF of type %s, not a program", plat, t)
	}
	entry := uint64(binary.Size(elf.Prog64{}))
	if h.Phnum == 0 || uint64(h.Phentsize) < entry {
		return fmt.Sprintf("the %s build has no program headers: not a program filex can start", plat)
	}
	end := h.Phoff + uint64(h.Phnum)*uint64(h.Phentsize)
	if end < h.Phoff || end > uint64(len(head)) {
		return fmt.Sprintf("the %s build's program headers are not within its first %d KiB, where the store reads them", plat, headMax>>10)
	}
	for i := uint64(0); i < uint64(h.Phnum); i++ {
		var p elf.Prog64
		off := h.Phoff + i*uint64(h.Phentsize)
		if binary.Read(bytes.NewReader(head[off:off+entry]), binary.LittleEndian, &p) != nil {
			return notFormat
		}
		if elf.ProgType(p.Type) == elf.PT_INTERP {
			return fmt.Sprintf("the %s build is dynamically linked (it names a program interpreter, PT_INTERP): the store lists self-contained builds, "+
				"statically linked, that start without the loader and C library they were linked against", plat)
		}
	}
	return ""
}

func elfArch(m elf.Machine) string {
	for arch, em := range elfMachine {
		if em == m {
			return arch
		}
	}
	return m.String()
}

func peProblem(plat, arch string, head []byte, notFormat string) string {
	if len(head) < 0x40 || !bytes.HasPrefix(head, []byte("MZ")) {
		return notFormat
	}
	// The PE header's offset is the DOS header's last field.
	off := uint64(binary.LittleEndian.Uint32(head[0x3c:0x40]))
	var fh pe.FileHeader
	size := uint64(binary.Size(fh))
	if off+4+size+2 > uint64(len(head)) || !bytes.Equal(head[off:off+4], []byte("PE\x00\x00")) ||
		binary.Read(bytes.NewReader(head[off+4:off+4+size]), binary.LittleEndian, &fh) != nil {
		return notFormat
	}
	if fh.Machine != peMachine[arch] {
		return fmt.Sprintf("the %s build is a Windows executable for %s, not for %s: each platform's build runs on that platform", plat, peArch(fh.Machine), arch)
	}
	if fh.Characteristics&pe.IMAGE_FILE_EXECUTABLE_IMAGE == 0 || fh.Characteristics&pe.IMAGE_FILE_DLL != 0 {
		return fmt.Sprintf("the %s build is a Windows library (DLL) or object, not a program", plat)
	}
	if fh.SizeOfOptionalHeader < 2 || binary.LittleEndian.Uint16(head[off+4+size:]) != pe32Plus {
		return fmt.Sprintf("the %s build is not a 64-bit (PE32+) Windows program", plat)
	}
	return ""
}

func peArch(m uint16) string {
	for arch, pm := range peMachine {
		if pm == m {
			return arch
		}
	}
	if m == pe.IMAGE_FILE_MACHINE_I386 {
		return "386"
	}
	return fmt.Sprintf("machine 0x%04x", m)
}

func machoProblem(plat, arch string, head []byte, notFormat string) string {
	if len(head) < 8 {
		return notFormat
	}
	want := machoMachine[arch]
	if binary.BigEndian.Uint32(head) == macho.MagicFat {
		// A universal binary: a table of slices, one per architecture. A Java
		// class file starts with the same four bytes, followed by its version
		// where the table's count would be - more slices than any binary has.
		n := uint64(binary.BigEndian.Uint32(head[4:8]))
		size := uint64(binary.Size(macho.FatArchHeader{}))
		if n == 0 || n > 32 || 8+n*size > uint64(len(head)) {
			return notFormat
		}
		r := bytes.NewReader(head[8 : 8+n*size])
		for i := uint64(0); i < n; i++ {
			var fa macho.FatArchHeader
			if binary.Read(r, binary.BigEndian, &fa) != nil {
				return notFormat
			}
			if fa.Cpu == want {
				return ""
			}
		}
		return fmt.Sprintf("the %s build is a universal macOS binary without a slice for %s", plat, arch)
	}
	var bo binary.ByteOrder
	switch le, be := binary.LittleEndian.Uint32(head), binary.BigEndian.Uint32(head); {
	case le == macho.Magic64:
		bo = binary.LittleEndian
	case be == macho.Magic64:
		bo = binary.BigEndian
	case le == macho.Magic32, be == macho.Magic32:
		return fmt.Sprintf("the %s build is a 32-bit macOS executable, not a 64-bit %s program", plat, arch)
	default:
		return notFormat
	}
	var fh macho.FileHeader
	if binary.Read(bytes.NewReader(head), bo, &fh) != nil {
		return notFormat
	}
	if fh.Cpu != want {
		return fmt.Sprintf("the %s build is a macOS executable for %s, not for %s: each platform's build runs on that platform", plat, machoArch(fh.Cpu), arch)
	}
	if fh.Type != macho.TypeExec {
		return fmt.Sprintf("the %s build is a Mach-O %s (a library, a bundle or an object), not a program", plat, fh.Type)
	}
	return ""
}

func machoArch(c macho.Cpu) string {
	for arch, mc := range machoMachine {
		if mc == c {
			return arch
		}
	}
	return c.String()
}

const (
	// maxFeedBytes is filex's own cap on a feed (plugin.maxFeedBytes).
	maxFeedBytes = 1 << 20
	// maxFeedNotes is what filex keeps of a feed's notes (plugin.maxNotesBytes):
	// the store keeps no more than filex will show.
	maxFeedNotes = 8 << 10
)

// feedBinary is one platform's build as a feed names it.
type feedBinary struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	// Signature is the publisher's own detached signature over the sha256;
	// filex reads it from the feed itself. The store signs every build with
	// its artifact key regardless.
	Signature string `json:"signature,omitempty"`
}

// feedDoc is filex's plugin.Feed, read leniently (a field a newer filex adds
// is not the store's to refuse).
type feedDoc struct {
	Name     string                `json:"name"`
	Version  string                `json:"version"`
	Filex    string                `json:"filex"`
	Notes    string                `json:"notes"`
	Binaries map[string]feedBinary `json:"binaries"`
}

// parsedFeed is what the resolver takes from a feed.
type parsedFeed struct {
	doc    feedDoc
	rng    filexrange.Range
	builds map[string]feedBinary
	notes  string
}

// feedError is a feed the store refuses, with the check code it fails.
type feedError struct {
	code string
	msg  string
}

func (e *feedError) Error() string { return e.msg }

func feedFail(code, format string, args ...any) *feedError {
	return &feedError{code: code, msg: fmt.Sprintf(format, args...)}
}

// parseFeed reads a feed and checks what the store stands on: a name filex
// accepts, a version, a range that parses, and a pinned build for every
// required platform and none for a platform nothing chooses.
func parseFeed(b []byte) (*parsedFeed, *feedError) {
	var p parsedFeed
	if err := json.Unmarshal(b, &p.doc); err != nil {
		return nil, feedFail(CodeManifestBad, "not a JSON feed: %v", err)
	}
	if !nameRe.MatchString(p.doc.Name) {
		return nil, feedFail(CodeManifestBad, "name %q must match %s", p.doc.Name, nameRe)
	}
	if strings.TrimSpace(p.doc.Version) == "" {
		return nil, feedFail(CodeManifestBad, "version is required")
	}
	rng, err := filexrange.Parse(strings.TrimSpace(p.doc.Filex))
	if err != nil {
		return nil, feedFail(CodeManifestBad, "the filex range does not parse: %v", err)
	}
	p.rng = rng
	known := map[string]bool{}
	for _, plat := range domain.StoragePlatforms {
		known[plat] = true
	}
	p.builds = map[string]feedBinary{}
	var unknown []string
	for plat, bin := range p.doc.Binaries {
		if !known[plat] {
			unknown = append(unknown, plat)
			continue
		}
		u, err := url.Parse(strings.TrimSpace(bin.URL))
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return nil, feedFail(CodeManifestBad, "the %s build's address %q is not an https address", plat, bin.URL)
		}
		sum := strings.ToLower(strings.TrimSpace(bin.SHA256))
		if len(sum) != 64 || strings.Trim(sum, "0123456789abcdef") != "" {
			return nil, feedFail(CodeSHA256Required, "the %s build pins no sha256 (64 hex characters); a download must be pinned by hash", plat)
		}
		p.builds[plat] = feedBinary{URL: u.String(), SHA256: sum, Signature: strings.TrimSpace(bin.Signature)}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, feedFail(CodePlatformUnknown, "builds for %s: filex chooses a build by one of %s",
			strings.Join(unknown, ", "), strings.Join(domain.StoragePlatforms, ", "))
	}
	var missing []string
	for _, plat := range domain.RequiredPlatforms {
		if _, ok := p.builds[plat]; !ok {
			missing = append(missing, plat)
		}
	}
	if len(missing) > 0 {
		return nil, feedFail(CodePlatformMissing, "no build for %s: the store lists a storage plugin with a build for %s at least",
			strings.Join(missing, ", "), strings.Join(domain.RequiredPlatforms, " and "))
	}
	p.notes = clipNotes(strings.TrimSpace(p.doc.Notes))
	return &p, nil
}

// clipNotes keeps the first 8 KiB of a feed's notes, cut on a character
// boundary - what filex keeps (plugin.clipNotes).
func clipNotes(s string) string {
	if len(s) <= maxFeedNotes {
		return s
	}
	cut := maxFeedNotes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// platforms is the feed's platforms in the store's order.
func (p *parsedFeed) platforms() []string {
	out := []string{}
	for _, plat := range domain.StoragePlatforms {
		if _, ok := p.builds[plat]; ok {
			out = append(out, plat)
		}
	}
	return out
}

// 3 + 4 for a storage plugin: the release's feed, then the consent file.
func (x *run) feedAndConsent() (bool, error) {
	var pf *parsedFeed
	ok, err := x.check(domain.CheckManifest, func() (domain.CheckStatus, string, string, any, error) {
		f, err := x.r.gh.ReleaseAsset(x.ctx, x.owner, x.name, x.res.Ref, FeedFile, maxFeedBytes)
		switch {
		case errors.Is(err, github.ErrNotFound):
			return failed(CodeManifestMissing, "no "+FeedFile+" attached to the release "+x.res.Ref+" of "+x.owner+"/"+x.name+
				": a storage plugin is listed from the feed its release publishes", nil)
		case errors.Is(err, safehttp.ErrTooLarge):
			return failed(CodeTooLarge, fmt.Sprintf("%s is larger than %d KiB", FeedFile, maxFeedBytes>>10), nil)
		case errors.Is(err, safehttp.ErrHostNotAllowed), errors.Is(err, safehttp.ErrNotHTTPS), errors.Is(err, safehttp.ErrPrivateAddress):
			return failed(CodeHostNotAllowed, FeedFile+" could not be read: "+err.Error(), nil)
		case err != nil && retryable(err):
			return "", "", "", nil, err
		case err != nil:
			return failed(CodeManifestBad, FeedFile+" could not be read: "+err.Error(), nil)
		}
		x.res.ManifestURL, x.res.ManifestSHA256 = f.URL, f.SHA256
		x.res.Manifest = json.RawMessage(f.Bytes)
		if !json.Valid(f.Bytes) {
			x.res.Manifest = nil
		}
		p, ferr := parseFeed(f.Bytes)
		if ferr != nil {
			return failed(ferr.code, FeedFile+": "+ferr.msg, map[string]any{"sha256": f.SHA256})
		}
		pf = p
		x.res.Name, x.res.Version, x.res.Kind = p.doc.Name, strings.TrimSpace(p.doc.Version), domain.KindStorage
		x.res.FilexRange = p.rng.Canonical()
		x.res.Permissions, x.res.Languages = []string{}, []string{}
		x.res.FeedNotes = p.notes
		return domain.CheckPassed, "", "", map[string]any{"sha256": f.SHA256, "bytes": len(f.Bytes), "kind": domain.KindStorage,
			"filex": p.rng.String(), "platforms": p.platforms()}, nil
	})
	if !ok || err != nil {
		return ok, err
	}
	x.feed = pf
	return x.consent()
}

// 6 for a storage plugin: every build the feed names, downloaded and held to
// its sha256, and an executable of its platform's own format.
func (x *run) binaries() (bool, error) {
	pf := x.feed
	return x.check(domain.CheckArtifacts, func() (domain.CheckStatus, string, string, any, error) {
		if pf == nil {
			return failed(CodeManifestBad, "the feed was not read", nil)
		}
		data := map[string]any{}
		builds := map[string]domain.Artifact{}
		for _, plat := range pf.platforms() {
			b := pf.builds[plat]
			hw := &headWriter{max: headMax}
			a, code, msg, err := x.fetchPinned(plat+" build", b.URL, b.SHA256, "the feed", x.r.lim.Binary, data, hw)
			if err != nil {
				return "", "", "", nil, err
			}
			if code != "" {
				return failed(code, msg, data)
			}
			if why := nativeProblem(plat, hw.head); why != "" {
				return failed(CodeBuildNotNative, why, data)
			}
			builds[plat] = *a
		}
		x.res.Binaries = builds
		return domain.CheckPassed, "", "", data, nil
	})
}

// fetchPinned downloads u through the guarded client (an allow-listed host,
// public addresses only, at most max bytes) and holds it to want, the sha256
// pinnedBy names. It answers the artifact, or a failed check's code and
// sentence, or a retryable error. data gets what was downloaded, under what.
// A non-nil w also receives the bytes as they are hashed (a build's first
// bytes, for its format).
func (x *run) fetchPinned(what, u, want, pinnedBy string, max int64, data map[string]any, w io.Writer) (*domain.Artifact, string, string, error) {
	var a *github.Asset
	var err error
	if w != nil {
		a, err = x.r.gh.DownloadTo(x.ctx, u, max, w)
	} else {
		a, err = x.r.gh.Download(x.ctx, u, max)
	}
	switch {
	case errors.Is(err, safehttp.ErrHostNotAllowed), errors.Is(err, safehttp.ErrNotHTTPS), errors.Is(err, safehttp.ErrPrivateAddress), errors.Is(err, safehttp.ErrBadURL):
		return nil, CodeHostNotAllowed, "the " + what + " address " + u + " is not one the store downloads from: " + err.Error(), nil
	case errors.Is(err, safehttp.ErrTooLarge):
		return nil, CodeTooLarge, fmt.Sprintf("the %s is larger than %d MiB", what, max>>20), nil
	case errors.Is(err, safehttp.ErrTooManyRedirects), errors.Is(err, github.ErrNotFound):
		return nil, CodeFetchFailed, "the " + what + " could not be downloaded from " + u + ": " + err.Error(), nil
	case err != nil && retryable(err):
		return nil, "", "", err
	case err != nil:
		return nil, CodeFetchFailed, "the " + what + " could not be downloaded from " + u + ": " + err.Error(), nil
	}
	data[what] = a
	if !strings.EqualFold(a.SHA256, strings.TrimSpace(want)) {
		return nil, CodeSHA256Mismatch, fmt.Sprintf("the %s's sha256 is %s, %s pins %s", what, a.SHA256, pinnedBy, strings.ToLower(strings.TrimSpace(want))), nil
	}
	return &domain.Artifact{URL: u, SHA256: strings.ToLower(a.SHA256), Size: a.Size}, "", "", nil
}
