package filexrange

import "testing"

func mustVersion(t *testing.T, s string) Version {
	t.Helper()
	v, err := ParseVersion(s)
	if err != nil {
		t.Fatalf("%q: %v", s, err)
	}
	return v
}

// filex's own cases: backend/internal/wasmplugin/compat_test.go
// TestFilexRange_TheGrammar. Same answers, or the store picks validators
// filex would not.
func TestRange_TheGrammarFilexReads(t *testing.T) {
	cases := []struct {
		rng  string
		host string
		want bool
	}{
		{">=0.47.0", "0.47.0", true},
		{">=0.47.0", "0.46.9", false},
		{">=0.47.0 <0.60.0", "0.59.3", true},
		{">=0.47.0 <0.60.0", "0.60.0", false},
		{"> 0.47.0", "0.47.1", true},
		{">0.47.0", "0.47.0", false},
		{"<=0.47.0", "0.47.0", true},
		{"0.47.0", "0.47.0", true},
		{"=0.47.0", "0.47.1", false},
		{"v0.47.0", "0.47.0", true},
		{">=0.47.0 <0.50.0 || >=0.52.0", "0.51.0", false},
		{">=0.47.0 <0.50.0 || >=0.52.0", "0.53.0", true},
		{"", "0.1.0", true},
	}
	for _, c := range cases {
		r, err := Parse(c.rng)
		if err != nil {
			t.Fatalf("%q: %v", c.rng, err)
		}
		if got := r.Admits(mustVersion(t, c.host)); got != c.want {
			t.Errorf("%q on %s = %v, want %v", c.rng, c.host, got, c.want)
		}
	}
	for _, bad := range []string{"^0.47.0", "~0.47", ">=0.47", ">=0.47.0-rc.1", ">=", ">=0.47.0 ||", "latest"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q must be refused, not read as something else", bad)
		}
	}
}

// filex's TestHostRelease_ReleaseCandidateCountsDevBuildDoesNot.
func TestHostRelease_ReleaseCandidateCountsDevBuildDoesNot(t *testing.T) {
	for _, c := range []struct {
		host     string
		enforced bool
		version  string
	}{
		{"v0.47.0 (abc1234, 2026-09-26T10:00:00Z)", true, "0.47.0"},
		{"0.47.0", true, "0.47.0"},
		{"0.47.0-rc.1", true, "0.47.0"},
		{"v0.47.0-beta.2", true, "0.47.0"},
		{"0.1.0-dev", false, ""},
		{"v0.46.0-5-g1a2b3c4", false, ""},
		{"dev", false, ""},
		{"", false, ""},
		// What the validators answered on 2026-10-03 (GET /api/capabilities).
		{"v0.50.0 (9873e3e4835a785a12b62a1ff725868c04103cb5, 2026-10-03T00:50:59Z)", true, "0.50.0"},
		{"v0.49.0 (9b183842053c5dd2b44fd225558fae6e2f5dd9f9, 2026-09-29T01:09:29Z)", true, "0.49.0"},
	} {
		v, ok := HostRelease(c.host)
		if ok != c.enforced {
			t.Errorf("%q: enforced = %v, want %v", c.host, ok, c.enforced)
		}
		if ok && v.Raw != c.version {
			t.Errorf("%q: version %q, want %q", c.host, v.Raw, c.version)
		}
	}
}

// filex's backend/internal/update/policy_test.go TestParseVersion.
func TestParseVersion_FilexCases(t *testing.T) {
	for _, tc := range []struct {
		in                  string
		major, minor, patch int
		pre                 string
		ok                  bool
	}{
		{"v0.7.6", 0, 7, 6, "", true},
		{"0.7.6", 0, 7, 6, "", true},
		{"1.2.3-rc.1", 1, 2, 3, "rc.1", true},
		{"0.1.0-dev", 0, 1, 0, "dev", true},
		{"v1.0.0+build7", 1, 0, 0, "", true},
		{"0.7", 0, 0, 0, "", false},
		{"latest", 0, 0, 0, "", false},
		{"", 0, 0, 0, "", false},
	} {
		v, err := ParseVersion(tc.in)
		if !tc.ok {
			if err == nil {
				t.Errorf("%q must not parse", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if [3]int{v.Major, v.Minor, v.Patch} != [3]int{tc.major, tc.minor, tc.patch} || v.Pre != tc.pre {
			t.Errorf("%q = %d.%d.%d-%q", tc.in, v.Major, v.Minor, v.Patch, v.Pre)
		}
	}
}

func TestCompare_PreReleaseBelowItsRelease(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.7.5", "0.7.6", -1},
		{"0.8.0", "0.7.9", 1},
		{"1.0.0", "0.99.99", 1},
		{"0.7.6", "v0.7.6", 0},
		{"0.7.6-rc.1", "0.7.6", -1},
		{"0.7.6", "0.7.6-rc.1", 1},
		{"0.7.6-rc.1", "0.7.6-rc.2", -1},
		{"1.0.0+a", "1.0.0+b", 0},
	} {
		if got := mustVersion(t, c.a).Compare(mustVersion(t, c.b)); got != c.want {
			t.Errorf("%s vs %s = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// filex's TestInstall_AnAppWhoseRangeLeavesThisFilexOutIsRefused: the
// older `min_filex` is held together with `filex`, and compat.requires reads
// ">=0.47.0 <0.60.0, >=0.43.0".
func TestCompat_MinFilexIsAFloorOnEveryAlternative(t *testing.T) {
	r, err := Compat(">=0.47.0 <0.60.0", "0.43.0")
	if err != nil {
		t.Fatal(err)
	}
	if r.String() != ">=0.47.0 <0.60.0, >=0.43.0" {
		t.Errorf("String() = %q", r.String())
	}
	r, err = Compat("", "0.48.0")
	if err != nil {
		t.Fatal(err)
	}
	if r.Admits(mustVersion(t, "0.47.0")) || !r.Admits(mustVersion(t, "0.48.0")) {
		t.Error("min_filex 0.48.0 must leave 0.47.0 out and let 0.48.0 in")
	}
	r, err = Compat(">=0.40.0 <0.45.0 || >=0.47.0", "v0.44.0")
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]bool{"0.43.0": false, "0.44.0": true, "0.46.0": false, "0.50.0": true} {
		if got := r.Admits(mustVersion(t, host)); got != want {
			t.Errorf("%s: %v, want %v", host, got, want)
		}
	}
	// What filex-convert 0.2.0 declares (filex ">=0.50.0", min_filex
	// "0.50.0"); filex 0.50.0 reported compat.requires ">=0.50.0, >=0.50.0".
	r, err = Compat(">=0.50.0", "0.50.0")
	if err != nil {
		t.Fatal(err)
	}
	if r.String() != ">=0.50.0, >=0.50.0" {
		t.Errorf("String() = %q", r.String())
	}
	if r.Admits(mustVersion(t, "0.49.0")) {
		t.Error("0.49.0 is outside >=0.50.0")
	}
	if _, err := Compat(">=0.47.0", "0.47"); err == nil {
		t.Error("a min_filex that is not x.y.z must be refused")
	}
	if r, _ := Compat("", ""); !r.Empty() || !r.Admits(mustVersion(t, "0.1.0")) {
		t.Error("no range admits every filex")
	}
}

// Canonical travels as one string (Resolution.FilexRange) and must parse
// back to the same answers, the min_filex floor included.
func TestCanonical_RoundTrips(t *testing.T) {
	for _, c := range []struct{ filex, min string }{
		{">=0.47.0 <0.60.0", "0.43.0"},
		{"> 0.47.0", ""},
		{"0.47.0", ""},
		{">=0.47.0 <0.50.0 || >=0.52.0", "0.48.0"},
		{"", "0.50.0"},
		{"", ""},
	} {
		r, err := Compat(c.filex, c.min)
		if err != nil {
			t.Fatal(err)
		}
		back, err := Parse(r.Canonical())
		if err != nil {
			t.Fatalf("%q: canonical %q does not parse: %v", c.filex, r.Canonical(), err)
		}
		for _, host := range []string{"0.42.0", "0.43.0", "0.47.0", "0.47.1", "0.48.0", "0.49.0", "0.50.0", "0.51.0", "0.52.0", "0.60.0", "1.0.0"} {
			v := mustVersion(t, host)
			if r.Admits(v) != back.Admits(v) {
				t.Errorf("%q + min %q: canonical %q answers differently on %s", c.filex, c.min, r.Canonical(), host)
			}
		}
	}
}

// The index carries the normal form: a comparator repeated (filex ">=X" and
// min_filex "X") or an alternative repeated is written once.
func TestCanonical_RepeatsAreWrittenOnce(t *testing.T) {
	for _, c := range []struct{ filex, min, want string }{
		{">=0.50.0", "0.50.0", ">=0.50.0"},
		{">=0.50.0 >=0.50.0 <0.60.0 >=0.50.0", "", ">=0.50.0 <0.60.0"},
		{">=0.48.0 <0.60.0 || >=0.70.0", "0.48.0", ">=0.48.0 <0.60.0 || >=0.70.0 >=0.48.0"},
		{">=0.47.0 || >=0.47.0", "", ">=0.47.0"},
		{"v0.47.0", "", "=0.47.0"},
		{">= 0.47.0", "v0.49.0", ">=0.47.0 >=0.49.0"},
	} {
		r, err := Compat(c.filex, c.min)
		if err != nil {
			t.Fatal(err)
		}
		if got := r.Canonical(); got != c.want {
			t.Errorf("%q + min_filex %q = %q, want %q", c.filex, c.min, got, c.want)
		}
	}
}
