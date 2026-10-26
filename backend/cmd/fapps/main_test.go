package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/index"
	"github.com/brf-tech/filex-apps/backend/internal/signing"
)

func runCmd(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestKeygen_TheSeedGoesOnlyToAPrivateFile(t *testing.T) {
	dir := t.TempDir()
	seedFile := filepath.Join(dir, "index.seed")
	code, out, errs := runCmd("keygen", "-id", "index-2026-10", "-out", seedFile)
	if code != 0 {
		t.Fatalf("%d %s", code, errs)
	}
	raw, err := os.ReadFile(seedFile)
	if err != nil {
		t.Fatal(err)
	}
	seed := strings.TrimSpace(string(raw))
	if len(seed) != 64 {
		t.Fatalf("seed %q", seed)
	}
	if strings.Contains(out, seed) {
		t.Error("the seed was printed")
	}
	k, err := signing.ParsePrivateKey("x", seed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "public: "+k.PublicHex()) || !strings.Contains(out, "id: index-2026-10") {
		t.Errorf("output %q", out)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(seedFile)
		if st.Mode().Perm() != 0o600 {
			t.Errorf("mode %v", st.Mode().Perm())
		}
	}
	if code, _, _ := runCmd("keygen", "-id", "again", "-out", seedFile); code == 0 {
		t.Error("an existing seed file was overwritten")
	}
	again, _ := os.ReadFile(seedFile)
	if string(again) != string(raw) {
		t.Error("the seed file changed")
	}
	if code, _, _ := runCmd("keygen", "-id", "x"); code != 2 {
		t.Error("keygen without -out must be a usage error (the seed is never printed)")
	}
}

func TestSign_WhatFilexVerifies(t *testing.T) {
	dir := t.TempDir()
	seedFile := filepath.Join(dir, "a.seed")
	if code, _, errs := runCmd("keygen", "-id", "artifact", "-out", seedFile); code != 0 {
		t.Fatal(errs)
	}
	raw, _ := os.ReadFile(seedFile)
	k, _ := signing.ParsePrivateKey("a", string(raw))
	pub, _ := signing.ParsePublicKey(k.PublicHex())
	module := filepath.Join(dir, "plugin.wasm")
	_ = os.WriteFile(module, []byte("\x00asm\x01\x00\x00\x00"), 0o644)
	code, out, errs := runCmd("sign", "-key", seedFile, module)
	if code != 0 {
		t.Fatal(errs)
	}
	sum := signing.SHA256Hex([]byte("\x00asm\x01\x00\x00\x00"))
	if err := signing.Verify([]ed25519.PublicKey{pub}, sum, strings.TrimSpace(out)); err != nil {
		t.Errorf("the file's signature: %v", err)
	}
	code, out, _ = runCmd("sign", "-key", seedFile, "-sha256", strings.ToUpper(sum))
	if code != 0 || signing.Verify([]ed25519.PublicKey{pub}, sum, strings.TrimSpace(out)) != nil {
		t.Errorf("-sha256 (upper case is lower-cased first): %d %q", code, out)
	}
	for _, bad := range [][]string{{"sign", "-key", seedFile}, {"sign", "-key", seedFile, "-sha256", sum, module}, {"sign", "-key", seedFile, "-sha256", "abc"}, {"sign", module}} {
		if code, _, _ := runCmd(bad...); code != 2 {
			t.Errorf("%v: exit %d, want a usage error", bad, code)
		}
	}
}

// storeServer serves a signed index the way the store does.
func storeServer(t *testing.T, k *signing.Key, doc index.Document, tamper bool) *httptest.Server {
	t.Helper()
	body, _ := json.MarshalIndent(doc, "", "  ")
	body = append(body, '\n')
	sig := k.SignBytes(body)
	if tamper {
		body = bytes.Replace(body, []byte(`"serial": `), []byte(`"serial":  `), 1)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/index.json":
			_, _ = w.Write(body)
		case "/v1/index.json.sig":
			_, _ = w.Write([]byte(sig + "\n"))
		case "/empty/index.json", "/empty/index.json.sig":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"index_not_ready"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestVerifyIndex_AsAFilexWould(t *testing.T) {
	seed := bytes.Repeat([]byte{7}, 32)
	k, _ := signing.NewKeyFromSeed("index-1", seed)
	other, _ := signing.NewKeyFromSeed("other", bytes.Repeat([]byte{8}, 32))
	now := time.Now().UTC()
	doc := index.Document{Schema: 1, Serial: 42, GeneratedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(60 * 24 * time.Hour).Format(time.RFC3339),
		Keys: []index.Key{{ID: "index-1", Use: "index", Ed25519: k.PublicHex(), Status: "active"}},
		Apps: []index.App{{Name: "sign", Versions: []index.Version{{Version: "0.3.0"}}}}}
	srv := storeServer(t, k, doc, false)
	url := srv.URL + "/v1/index.json"

	code, out, errs := runCmd("verify-index", "-key", k.PublicHex(), url)
	if code != 0 || !strings.Contains(out, "ok: serial 42") || !strings.Contains(out, "1 apps, 1 versions") {
		t.Fatalf("%d %q %q", code, out, errs)
	}
	if code, _, _ := runCmd("verify-index", "-key", other.PublicHex()+","+k.PublicHex(), url); code != 0 {
		t.Error("any of several trusted keys verifies")
	}
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"untrusted key":  {[]string{"verify-index", "-key", other.PublicHex(), url}, "signature"},
		"rollback":       {[]string{"verify-index", "-key", k.PublicHex(), "-min-serial", "43", url}, "rollback"},
		"not ready":      {[]string{"verify-index", "-key", k.PublicHex(), srv.URL + "/empty/index.json"}, "503"},
		"tampered":       {[]string{"verify-index", "-key", k.PublicHex(), storeServer(t, k, doc, true).URL + "/v1/index.json"}, "signature"},
		"expired":        {[]string{"verify-index", "-key", k.PublicHex(), storeServer(t, k, func() index.Document { d := doc; d.ExpiresAt = now.Add(-time.Hour).Format(time.RFC3339); return d }(), false).URL + "/v1/index.json"}, "expired"},
		"another schema": {[]string{"verify-index", "-key", k.PublicHex(), storeServer(t, k, func() index.Document { d := doc; d.Schema = 2; return d }(), false).URL + "/v1/index.json"}, "schema 2"},
	} {
		code, _, errs := runCmd(c.args...)
		if code != 1 || !strings.Contains(errs, c.want) {
			t.Errorf("%s: exit %d %q, want 1 and %q", name, code, errs, c.want)
		}
	}
	if code, _, _ := runCmd("verify-index", url); code != 2 {
		t.Error("without -key: a usage error (the trusted key never comes from the index)")
	}
}

func TestVectors_TheGoSignerReproducesThem(t *testing.T) {
	file := "../../internal/signing/testdata/vectors.json"
	code, out, errs := runCmd("vectors", "-file", file)
	if code != 0 || strings.Count(out, "ok   ") < 2 {
		t.Fatalf("%d %q %q", code, out, errs)
	}
	b, _ := os.ReadFile(file)
	bad := filepath.Join(t.TempDir(), "v.json")
	_ = os.WriteFile(bad, bytes.Replace(b, []byte(`"signature_hex": "98`), []byte(`"signature_hex": "99`), 1), 0o644)
	code, out, _ = runCmd("vectors", "-file", bad)
	if code != 1 || !strings.Contains(out, "FAIL vector-1: signature differs") {
		t.Errorf("a changed vector: %d %q", code, out)
	}
}

func TestRun_Usage(t *testing.T) {
	if code, _, _ := runCmd(); code != 2 {
		t.Error("no command")
	}
	if code, _, _ := runCmd("frobnicate"); code != 2 {
		t.Error("unknown command")
	}
	if code, out, _ := runCmd("help"); code != 0 || !strings.Contains(out, "verify-index") {
		t.Error("help")
	}
}
