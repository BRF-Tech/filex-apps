// Package filexrange reads an app's `filex` range and orders versions the
// way filex itself does, so the store picks the validators a range admits
// and judges versions with filex's answers, not with a second opinion.
//
// ⚠⚠ ONE GRAMMAR, TWO PROGRAMS. This is a port of filex's
// backend/internal/wasmplugin/compat.go (parseFilexRange, compatRange,
// hostRelease) and backend/internal/update/semver.go (ParseVersion, Compare).
// The tests carry filex's own cases; when filex changes its grammar, this
// package changes with it in the same round.
//
//	range       := alternative ( "||" alternative )*
//	alternative := comparator ( " " comparator )*      every one must hold
//	comparator  := [op] version                        op: >= > <= < =
//	version     := MAJOR.MINOR.PATCH                    a leading "v" is fine
//
// No `^`, `~`, `x` wildcards or pre-release versions in a range. The older
// `min_filex` ("0.43.0" = ">=0.43.0") is honoured together with `filex`
// (both must hold): see Compat.
package filexrange

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed semantic version, filex's update.Version. Pre-release
// and build metadata are kept for display; Compare ranks a pre-release below
// its release.
type Version struct {
	Major, Minor, Patch int
	Pre                 string // "rc.1" in 1.2.3-rc.1 (without the dash)
	Raw                 string // exactly as parsed, e.g. "v0.7.6"
}

// ParseVersion accepts "v0.7.6", "0.7.6", "0.7.6-rc.1", "0.1.0-dev" and
// "1.0.0+build7" (build metadata is dropped), exactly as filex does.
func ParseVersion(s string) (Version, error) {
	raw := strings.TrimSpace(s)
	v := Version{Raw: raw}
	body := strings.TrimPrefix(raw, "v")
	if i := strings.IndexByte(body, '+'); i >= 0 {
		body = body[:i]
	}
	if i := strings.IndexByte(body, '-'); i >= 0 {
		v.Pre = body[i+1:]
		body = body[:i]
	}
	parts := strings.Split(body, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("not a semantic version: %q", s)
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("not a semantic version: %q", s)
		}
		nums[i] = n
	}
	v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]
	return v, nil
}

// String is the canonical "v0.7.6" / "v0.7.6-rc.1" form.
func (v Version) String() string {
	s := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Plain is "x.y.z" without a "v" or a pre-release.
func (v Version) Plain() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Compare returns -1, 0 or +1 ordering v against o. A pre-release sorts below
// the same x.y.z release; two pre-releases compare as strings (filex's rule).
func (v Version) Compare(o Version) int {
	switch {
	case v.Major != o.Major:
		return sign(v.Major - o.Major)
	case v.Minor != o.Minor:
		return sign(v.Minor - o.Minor)
	case v.Patch != o.Patch:
		return sign(v.Patch - o.Patch)
	}
	switch {
	case v.Pre == o.Pre:
		return 0
	case v.Pre == "":
		return 1
	case o.Pre == "":
		return -1
	}
	return strings.Compare(v.Pre, o.Pre)
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

type comparator struct {
	op string // ">=", ">", "<=", "<", "="
	v  Version
}

func (c comparator) holds(host Version) bool {
	d := host.Compare(c.v)
	switch c.op {
	case ">=":
		return d >= 0
	case ">":
		return d > 0
	case "<=":
		return d <= 0
	case "<":
		return d < 0
	default:
		return d == 0
	}
}

// Range is a parsed range: any alternative whose comparators all hold. The
// zero value (no alternatives) admits every filex.
type Range struct {
	alts [][]comparator
	text string
}

var ops = []string{">=", "<=", ">", "<", "="}

func parseRangeVersion(s string) (Version, error) {
	v, err := ParseVersion(s)
	if err != nil {
		return Version{}, fmt.Errorf("%q is not a version (write it as MAJOR.MINOR.PATCH, e.g. 0.47.0)", s)
	}
	if v.Pre != "" {
		return Version{}, fmt.Errorf("%q: a range names releases, not pre-releases", s)
	}
	return v, nil
}

// Parse reads a `filex` range. Empty means no constraint.
func Parse(s string) (Range, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Range{}, nil
	}
	out := Range{text: strings.Join(strings.Fields(s), " ")}
	for _, altText := range strings.Split(s, "||") {
		fields := strings.Fields(altText)
		if len(fields) == 0 {
			return Range{}, fmt.Errorf("%q has an empty alternative around ||", s)
		}
		var alt []comparator
		for i := 0; i < len(fields); i++ {
			tok := fields[i]
			op := ""
			for _, o := range ops {
				if strings.HasPrefix(tok, o) {
					op = o
					break
				}
			}
			ver := strings.TrimPrefix(tok, op)
			if ver == "" {
				if i+1 >= len(fields) {
					return Range{}, fmt.Errorf("%q ends with an operator and no version", s)
				}
				i++
				ver = fields[i]
			}
			if op == "" {
				op = "="
			}
			v, err := parseRangeVersion(ver)
			if err != nil {
				return Range{}, err
			}
			alt = append(alt, comparator{op: op, v: v})
		}
		out.alts = append(out.alts, alt)
	}
	return out, nil
}

// Compat is everything a manifest says about which filex it needs: `filex`
// and the older `min_filex`, both of which must hold (filex's compatRange).
func Compat(filex, minFilex string) (Range, error) {
	rng, err := Parse(filex)
	if err != nil {
		return Range{}, fmt.Errorf("filex: %w", err)
	}
	floorText := strings.TrimPrefix(strings.TrimSpace(minFilex), "v")
	if floorText == "" {
		return rng, nil
	}
	v, err := parseRangeVersion(floorText)
	if err != nil {
		return Range{}, fmt.Errorf("min_filex: %w", err)
	}
	floor := comparator{op: ">=", v: v}
	if len(rng.alts) == 0 {
		return Range{alts: [][]comparator{{floor}}, text: ">=" + floorText}, nil
	}
	for i := range rng.alts {
		rng.alts[i] = append(rng.alts[i], floor)
	}
	rng.text += ", >=" + floorText
	return rng, nil
}

// Empty reports whether the range admits every filex.
func (r Range) Empty() bool { return len(r.alts) == 0 }

// Admits reports whether the range lets host in.
func (r Range) Admits(host Version) bool {
	if len(r.alts) == 0 {
		return true
	}
	for _, alt := range r.alts {
		ok := true
		for _, c := range alt {
			if !c.holds(host) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// String is the range as filex shows it (compat.requires): the text as
// written, with ", >=<min_filex>" when Compat added a floor.
func (r Range) String() string { return r.text }

// Canonical writes the range back in the grammar Parse reads, with any
// min_filex floor folded into every alternative, so it can travel as one
// string (domain.Resolution.FilexRange, the index's `filex`) and be parsed
// again unchanged. A comparator repeated within an alternative, and an
// alternative repeated whole, are written once: filex-convert's
// filex ">=0.50.0" + min_filex "0.50.0" is ">=0.50.0", not ">=0.50.0 >=0.50.0".
// Order is kept otherwise; the answers are the same.
func (r Range) Canonical() string {
	alts := make([]string, 0, len(r.alts))
	seenAlt := map[string]bool{}
	for _, alt := range r.alts {
		cs := make([]string, 0, len(alt))
		seen := map[string]bool{}
		for _, c := range alt {
			t := c.op + c.v.Plain()
			if !seen[t] {
				seen[t] = true
				cs = append(cs, t)
			}
		}
		a := strings.Join(cs, " ")
		if !seenAlt[a] {
			seenAlt[a] = true
			alts = append(alts, a)
		}
	}
	return strings.Join(alts, " || ")
}

// HostRelease reads the version a filex reports (GET /api/capabilities
// `version`, e.g. "v0.50.0 (9873e3e4…, 2026-10-03T00:50:59Z)") as filex's
// hostRelease does: a release or a named pre-release (rc, beta, alpha)
// counts as its x.y.z and ranges are enforced; anything else is a
// development build on which filex enforces no range (enforced = false).
func HostRelease(reported string) (v Version, enforced bool) {
	f := strings.Fields(reported)
	if len(f) == 0 {
		return Version{}, false
	}
	v, err := ParseVersion(f[0])
	if err != nil {
		return Version{}, false
	}
	if v.Pre != "" {
		named := false
		for _, p := range []string{"rc", "beta", "alpha"} {
			if v.Pre == p || strings.HasPrefix(v.Pre, p+".") {
				named = true
				break
			}
		}
		if !named {
			return Version{}, false
		}
		v.Pre = ""
	}
	v.Raw = v.Plain()
	return v, true
}
