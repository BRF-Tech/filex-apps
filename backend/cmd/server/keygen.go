package main

// fapps-server keygen -out <file> [-suffix <YYYY-MM>] [-store <https origin>]
//
// makes every key a new store needs, once: the index, artifact and license
// signing keys (ed25519 seeds, hex, with ids like index-2026-10),
// FAPPS_DATA_KEY and FAPPS_SESSION_KEY (32 random bytes each, base64) and
// the Web Push pair (FAPPS_VAPID_PUBLIC_KEY / _PRIVATE_KEY). The secrets go
// ONLY to the file, as .env lines: created with mode 0600, never over an
// existing file. Standard output carries only what is public: each signing
// key's public half and fingerprint (the lower-hex SHA-256 of its 32 raw
// bytes, what filex shows when it asks whether to trust a store), the keys
// document to publish beside the store (the keys of /v1/keys.json with
// their fingerprints), and the settings a filex administrator gives a filex
// that trusts this store by configuration.
//
// It reads no configuration and opens no database: it can run in a fresh
// container, as the owner of the folder it writes to (the image's own user
// may not write a mounted folder of the host's):
//
//	docker run --rm --user "$(id -u):$(id -g)" -v "$PWD:/out" <image> keygen -out /out/fapps-keys.env

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/httpapi"
	"github.com/brf-tech/filex-apps/backend/internal/index"
	"github.com/brf-tech/filex-apps/backend/internal/install"
	"github.com/brf-tech/filex-apps/backend/internal/notify"
	"github.com/brf-tech/filex-apps/backend/internal/signing"
)

// keySuffixRe is the part after "<use>-" in a generated key id.
var keySuffixRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// fingerprintNote is the keys document's explanation of its sha256 values
// (the same words as the production store's published keys document).
const fingerprintNote = "lower-hex sha256 of the 32 raw ed25519 public key bytes (what filex shows when it asks to trust a store)"

// keysDocument is what an operator publishes apart from the store (signed
// with a key of their own), so an administrator can check the store's keys
// from a second source before trusting it.
type keysDocument struct {
	Store       string        `json:"store"`
	Generated   string        `json:"generated"`
	Fingerprint string        `json:"fingerprint"`
	Keys        []documentKey `json:"keys"`
}

// documentKey is a key as /v1/keys.json lists it, with its fingerprint.
type documentKey struct {
	httpapi.KeyView
	SHA256 string `json:"sha256"`
}

// generatedKeys are one keygen run's keys.
type generatedKeys struct {
	signing  []generatedKey // index, artifact, license
	data     string         // FAPPS_DATA_KEY
	session  string         // FAPPS_SESSION_KEY
	vapidPub string
	vapidKey string
}

type generatedKey struct {
	use, id, seedHex, publicHex, fingerprint string
}

// keygen runs `fapps-server keygen` and returns the exit code.
func keygen(args []string, stdout, stderr io.Writer, now time.Time) int {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	out := fs.String("out", "", "the file the secrets are written to (mode 0600; must not exist)")
	suffix := fs.String("suffix", now.UTC().Format("2006-01"), "the key ids' suffix: index-<suffix>, artifact-<suffix>, license-<suffix>")
	store := fs.String("store", firstNonEmpty(os.Getenv("FAPPS_PUBLIC_URL"), os.Getenv("APP_URL")),
		"the store's public https address, for the keys document and the filex settings (default FAPPS_PUBLIC_URL or APP_URL)")
	usage := func(why string) int {
		fmt.Fprintf(stderr, "fapps-server keygen: %s\n%s", why, operatorUsage)
		return 2
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return usage("bad arguments")
	}
	if strings.TrimSpace(*out) == "" {
		return usage("-out is required: the secrets are written to a file, never printed")
	}
	if !keySuffixRe.MatchString(*suffix) {
		return usage(fmt.Sprintf("-suffix %q: letters, digits, '.', '_' and '-', at most 32", *suffix))
	}
	origin, err := storeOrigin(*store)
	if err != nil {
		return usage(err.Error())
	}

	// The file first: an existing one stops the run before anything is made.
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		why := ""
		switch {
		case errors.Is(err, os.ErrExist):
			why = " (an existing file is never overwritten)"
		case errors.Is(err, os.ErrPermission):
			// The image runs as an unprivileged user: a mounted folder of the
			// host's is not its to write.
			why = ` (this user may not write there; in a container, run it as the folder's owner: docker run --user "$(id -u):$(id -g)" ...)`
		}
		fmt.Fprintf(stderr, "fapps-server keygen: %v%s\n", err, why)
		return 1
	}
	keys, err := makeKeys(*suffix)
	if err == nil {
		_, err = io.WriteString(f, secretsFile(keys, origin, now))
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(*out)
		fmt.Fprintf(stderr, "fapps-server keygen: %v\n", err)
		return 1
	}
	if err := printPublic(stdout, keys, *out, origin, now); err != nil {
		fmt.Fprintf(stderr, "fapps-server keygen: %v\n", err)
		return 1
	}
	return 0
}

// makeKeys makes every key, through the same code the server reads them
// with (internal/signing, the fingerprint of internal/install, the VAPID
// pair of internal/notify).
func makeKeys(suffix string) (*generatedKeys, error) {
	k := &generatedKeys{}
	for _, use := range []string{index.UseIndex, index.UseArtifact, index.UseLicense} {
		seed := make([]byte, 32)
		if _, err := rand.Read(seed); err != nil {
			return nil, err
		}
		key, err := signing.NewKeyFromSeed(use+"-"+suffix, seed)
		if err != nil {
			return nil, err
		}
		pub, err := signing.ParsePublicKey(key.PublicHex())
		if err != nil {
			return nil, err
		}
		k.signing = append(k.signing, generatedKey{use: use, id: key.ID, seedHex: hex.EncodeToString(seed),
			publicHex: key.PublicHex(), fingerprint: install.KeyFingerprint(pub)})
	}
	for _, dst := range []*string{&k.data, &k.session} {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		*dst = base64.StdEncoding.EncodeToString(b)
	}
	var err error
	if k.vapidPub, k.vapidKey, err = notify.GenerateVAPID(); err != nil {
		return nil, err
	}
	return k, nil
}

// envName is a signing key's variable: FAPPS_<USE>_KEY.
func envName(use string) string { return "FAPPS_" + strings.ToUpper(use) + "_KEY" }

// secretsFile is the file's text: .env lines with what each is.
func secretsFile(k *generatedKeys, origin string, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# The keys of a filex app store%s, made by fapps-server keygen on %s.\n", forStore(origin), now.UTC().Format(time.RFC3339))
	b.WriteString("# SECRETS: keep this file out of every repository, and keep a copy where a lost server cannot take it.\n")
	b.WriteString("# The signing keys sign the index, the apps' files and license answers; a filex trusts them by their fingerprints.\n")
	b.WriteString("# FAPPS_DATA_KEY must never change on a store with data: license keys and saved addresses depend on it.\n")
	for _, s := range k.signing {
		fmt.Fprintf(&b, "%s_ID=%s\n%s=%s\n", envName(s.use), s.id, envName(s.use), s.seedHex)
	}
	fmt.Fprintf(&b, "FAPPS_DATA_KEY=%s\n", k.data)
	fmt.Fprintf(&b, "FAPPS_SESSION_KEY=%s\n", k.session)
	b.WriteString("# The Web Push pair of the browser notifications (the public half is not a secret).\n")
	fmt.Fprintf(&b, "FAPPS_VAPID_PUBLIC_KEY=%s\nFAPPS_VAPID_PRIVATE_KEY=%s\n", k.vapidPub, k.vapidKey)
	return b.String()
}

// printPublic writes what is public: the keys, the keys document and a
// filex's settings. Never a secret.
func printPublic(w io.Writer, k *generatedKeys, out, origin string, now time.Time) error {
	fmt.Fprintf(w, "The secrets are in %s (mode 0600): ", out)
	names := []string{}
	for _, s := range k.signing {
		names = append(names, envName(s.use)+"_ID", envName(s.use))
	}
	names = append(names, "FAPPS_DATA_KEY", "FAPPS_SESSION_KEY", "FAPPS_VAPID_PUBLIC_KEY", "FAPPS_VAPID_PRIVATE_KEY")
	fmt.Fprintf(w, "%s.\nGive them to the store's environment; they are never printed.\n\n", strings.Join(names, ", "))

	fmt.Fprintf(w, "Public keys (fingerprint: %s):\n", fingerprintNote)
	doc := keysDocument{Store: origin, Generated: now.UTC().Format("2006-01-02"), Fingerprint: fingerprintNote}
	for _, s := range k.signing {
		fmt.Fprintf(w, "  %-8s %-24s ed25519 %s\n  %-8s %-24s sha256  %s\n", s.use, s.id, s.publicHex, "", "", s.fingerprint)
		doc.Keys = append(doc.Keys, documentKey{KeyView: httpapi.KeyView{ID: s.id, Use: s.use, Ed25519: s.publicHex, Status: "active"},
			SHA256: s.fingerprint})
	}
	fmt.Fprintf(w, "  Web Push (FAPPS_VAPID_PUBLIC_KEY): %s\n\n", k.vapidPub)

	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	fmt.Fprintf(w, "The keys document: publish it apart from the store (signed with a key of yours), so an administrator\n"+
		"can check the fingerprints from a second source before trusting the store (sha256 of the text below: %s):\n%s\n\n",
		hex.EncodeToString(sum[:]), body)

	var idx, lic, art string
	for _, s := range k.signing {
		switch s.use {
		case index.UseIndex:
			idx = s.publicHex
		case index.UseLicense:
			lic = s.publicHex
		case index.UseArtifact:
			art = s.publicHex
		}
	}
	b := "<the store's https address>"
	if origin != "" {
		b = origin
	}
	fmt.Fprintf(w, "A filex that trusts this store by configuration (instead of its administrator comparing the fingerprints):\n"+
		"  FILEX_APP_STORE_URLS=%s\n  FILEX_APP_STORE_KEYS=index:%s,license:%s\n"+
		"and, to take the modules and storage plugin builds this store signs as signed, the artifact key in its list:\n"+
		"  FILEX_PLUGIN_TRUSTED_KEYS=%s\n", b, idx, lic, art)
	return nil
}

// storeOrigin is the https origin of the store's public address ("" when
// none was given); anything but an https address is refused.
func storeOrigin(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", nil
	}
	u, err := url.Parse(addr)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", errors.New("-store: " + addr + " is not the store's https address")
	}
	return "https://" + strings.ToLower(u.Host), nil
}

func forStore(origin string) string {
	if origin == "" {
		return ""
	}
	return " (" + origin + ")"
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}
