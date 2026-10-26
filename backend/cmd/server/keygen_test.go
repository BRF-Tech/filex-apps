package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/config"
	"github.com/brf-tech/filex-apps/backend/internal/notify"
	"github.com/brf-tech/filex-apps/backend/internal/session"
	"github.com/brf-tech/filex-apps/backend/internal/signing"
)

// readEnvFile reads KEY=value lines (comments and blank lines skipped).
func readEnvFile(t *testing.T, p string) map[string]string {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("not KEY=value: %q", line)
		}
		if _, dup := out[k]; dup {
			t.Fatalf("%s twice", k)
		}
		out[k] = v
	}
	return out
}

// keygen writes every secret a store needs to the file only, each in the
// form the server reads; standard output carries only public halves, their
// fingerprints (filex's: the sha256 of the 32 raw bytes) and a filex's
// settings.
func TestKeygen_SecretsInTheFileOnlyPublicHalvesOnScreen(t *testing.T) {
	out := filepath.Join(t.TempDir(), "fapps-keys.env")
	var stdout, stderr bytes.Buffer
	at := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	if code := keygen([]string{"-out", out, "-store", "https://Apps.Example.com/"}, &stdout, &stderr, at); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	env := readEnvFile(t, out)
	screen := stdout.String()

	secrets := []string{"FAPPS_INDEX_KEY", "FAPPS_ARTIFACT_KEY", "FAPPS_LICENSE_KEY", "FAPPS_DATA_KEY", "FAPPS_SESSION_KEY", "FAPPS_VAPID_PRIVATE_KEY"}
	want := append([]string{"FAPPS_INDEX_KEY_ID", "FAPPS_ARTIFACT_KEY_ID", "FAPPS_LICENSE_KEY_ID", "FAPPS_VAPID_PUBLIC_KEY"}, secrets...)
	if len(env) != len(want) {
		t.Fatalf("file has %d variables, want %d: %v", len(env), len(want), env)
	}
	for _, name := range secrets {
		v := env[name]
		if len(v) < 32 {
			t.Fatalf("%s = %q", name, v)
		}
		if strings.Contains(screen, v) || strings.Contains(stderr.String(), v) {
			t.Errorf("%s was printed", name)
		}
	}

	// Each key in the form the server reads it, under its id.
	type pub struct{ id, hex, fpr string }
	pubs := map[string]pub{}
	for _, use := range []string{"index", "artifact", "license"} {
		name := "FAPPS_" + strings.ToUpper(use) + "_KEY"
		id := env[name+"_ID"]
		if id != use+"-2026-10" {
			t.Errorf("%s_ID = %q", name, id)
		}
		k, err := signing.ParsePrivateKey(id, env[name])
		if err != nil || len(env[name]) != 64 {
			t.Fatalf("%s: %v", name, err)
		}
		raw, _ := hex.DecodeString(k.PublicHex())
		sum := sha256.Sum256(raw)
		pubs[use] = pub{id, k.PublicHex(), hex.EncodeToString(sum[:])}
		if !strings.Contains(screen, k.PublicHex()) || !strings.Contains(screen, hex.EncodeToString(sum[:])) {
			t.Errorf("%s: the public key or its fingerprint is not on the screen", use)
		}
	}
	if pubs["index"].hex == pubs["artifact"].hex || pubs["artifact"].hex == pubs["license"].hex {
		t.Fatal("two uses share a key")
	}
	if b, err := config.ParseDataKey(env["FAPPS_DATA_KEY"]); err != nil || len(b) != 32 {
		t.Errorf("FAPPS_DATA_KEY: %v", err)
	}
	if b, err := session.ParseSecret(env["FAPPS_SESSION_KEY"]); err != nil || len(b) != 32 {
		t.Errorf("FAPPS_SESSION_KEY: %v", err)
	}
	if env["FAPPS_DATA_KEY"] == env["FAPPS_SESSION_KEY"] {
		t.Error("the data key and the session key are the same")
	}
	if _, err := notify.ParseVAPID(env["FAPPS_VAPID_PUBLIC_KEY"], env["FAPPS_VAPID_PRIVATE_KEY"], "https://apps.example.com"); err != nil {
		t.Errorf("VAPID pair: %v", err)
	}
	if !strings.Contains(screen, env["FAPPS_VAPID_PUBLIC_KEY"]) {
		t.Error("the VAPID public key is not on the screen")
	}

	// A filex's settings, as filex reads them (index:<key>,license:<key>).
	for _, line := range []string{
		"FILEX_APP_STORE_URLS=https://apps.example.com\n",
		"FILEX_APP_STORE_KEYS=index:" + pubs["index"].hex + ",license:" + pubs["license"].hex + "\n",
		"FILEX_PLUGIN_TRUSTED_KEYS=" + pubs["artifact"].hex + "\n",
	} {
		if !strings.Contains(screen, line) {
			t.Errorf("missing %q in:\n%s", line, screen)
		}
	}

	// The keys document: the keys of /v1/keys.json with their fingerprints.
	start, end := strings.Index(screen, "{\n"), strings.Index(screen, "\n}\n")
	if start < 0 || end < start {
		t.Fatalf("no keys document in:\n%s", screen)
	}
	var doc struct {
		Store     string `json:"store"`
		Generated string `json:"generated"`
		Keys      []struct {
			ID, Use, Ed25519, Status, SHA256 string
		} `json:"keys"`
	}
	if err := json.Unmarshal([]byte(screen[start:end+2]), &doc); err != nil {
		t.Fatalf("keys document: %v", err)
	}
	if doc.Store != "https://apps.example.com" || doc.Generated != "2026-10-26" || len(doc.Keys) != 3 {
		t.Fatalf("keys document %+v", doc)
	}
	for _, k := range doc.Keys {
		p := pubs[k.Use]
		if k.ID != p.id || k.Ed25519 != p.hex || k.SHA256 != p.fpr || k.Status != "active" {
			t.Errorf("keys document %+v, want %+v", k, p)
		}
	}

	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(out); st.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", st.Mode().Perm())
		}
	}

	// Never over an existing file, and the file stays as it was.
	before, _ := os.ReadFile(out)
	stdout.Reset()
	stderr.Reset()
	if code := keygen([]string{"-out", out}, &stdout, &stderr, at); code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "never overwritten") {
		t.Fatalf("over an existing file: exit %d, %q %q", code, stdout.String(), stderr.String())
	}
	if after, _ := os.ReadFile(out); !bytes.Equal(before, after) {
		t.Fatal("the existing file changed")
	}
}

// A second run makes other keys (nothing is derived from the date or the
// name), and the usage errors stop before any file is made.
func TestKeygen_FreshKeysAndUsageErrors(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	a, b := filepath.Join(dir, "a.env"), filepath.Join(dir, "b.env")
	var stdout, stderr bytes.Buffer
	t.Setenv("FAPPS_PUBLIC_URL", "")
	t.Setenv("APP_URL", "")
	if keygen([]string{"-out", a, "-suffix", "2026-q1"}, &stdout, &stderr, at) != 0 || keygen([]string{"-out", b}, &stdout, &stderr, at) != 0 {
		t.Fatal(stderr.String())
	}
	ea, eb := readEnvFile(t, a), readEnvFile(t, b)
	if ea["FAPPS_INDEX_KEY_ID"] != "index-2026-q1" || eb["FAPPS_INDEX_KEY_ID"] != "index-2026-01" {
		t.Errorf("ids %q %q", ea["FAPPS_INDEX_KEY_ID"], eb["FAPPS_INDEX_KEY_ID"])
	}
	for k := range ea {
		if !strings.HasSuffix(k, "_ID") && ea[k] == eb[k] {
			t.Errorf("%s is the same in two runs", k)
		}
	}
	if !strings.Contains(stdout.String(), "FILEX_APP_STORE_URLS=<the store's https address>") {
		t.Errorf("without a store address: %s", stdout.String())
	}

	for _, args := range [][]string{
		{},
		{"-suffix", "2026-10"},
		{"-out", filepath.Join(dir, "c.env"), "extra"},
		{"-out", filepath.Join(dir, "c.env"), "-suffix", "bad suffix"},
		{"-out", filepath.Join(dir, "c.env"), "-suffix", ""},
		{"-out", filepath.Join(dir, "c.env"), "-store", "http://apps.example.com"},
		{"-out", filepath.Join(dir, "c.env"), "-nonsense"},
	} {
		stderr.Reset()
		if code := keygen(args, &stdout, &stderr, at); code != 2 || !strings.Contains(stderr.String(), "usage:") {
			t.Errorf("%v: exit %d %q", args, code, stderr.String())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "c.env")); !os.IsNotExist(err) {
		t.Fatal("a usage error left a file")
	}
	// Routed as an operator command (never a second server in the container).
	if !isOperatorCommand("keygen") {
		t.Fatal("keygen is not an operator command")
	}
}
