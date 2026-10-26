// Command fapps is the store operator's tool:
//
//	fapps keygen -id <key id> -out <seed file>
//	    makes an ed25519 key: the seed goes ONLY to the file (mode 0600, never
//	    overwritten), the public key and its id to the screen.
//	fapps sign -key <seed file> (-sha256 <hex> | <file>)
//	    signs the lower-hex sha256 of a file (or a given sha256) the way filex
//	    verifies it (internal/signing); prints the signature as hex.
//	fapps verify-index -key <public hex>[,<public hex>] [-min-serial N] <url>
//	    fetches <url> and <url>.sig and verifies them as a filex would: the
//	    signature over the body's sha256 against the trusted keys given here
//	    (never the keys the index lists), schema 1, not expired, serial not
//	    below -min-serial.
//	fapps vectors [-file <vectors.json>]
//	    re-derives the signing test vectors with the Go signer.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/index"
	"github.com/brf-tech/filex-apps/backend/internal/signing"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `usage:
  fapps keygen -id <key id> -out <seed file>
  fapps sign -key <seed file> (-sha256 <hex> | <file>)
  fapps verify-index -key <public hex>[,...] [-min-serial N] <url>
  fapps vectors [-file <vectors.json>]
`

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "keygen":
		err = keygen(args[1:], stdout)
	case "sign":
		err = sign(args[1:], stdout)
	case "verify-index":
		err = verifyIndex(args[1:], stdout, time.Now)
	case "vectors":
		err = vectors(args[1:], stdout)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "fapps: unknown command %q\n%s", args[0], usage)
		return 2
	}
	if err != nil {
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintf(stderr, "fapps %s: %v\n%s", args[0], err, usage)
			return 2
		}
		fmt.Fprintf(stderr, "fapps %s: %v\n", args[0], err)
		return 1
	}
	return 0
}

type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func keygen(args []string, stdout io.Writer) error {
	fs := flags("keygen")
	id := fs.String("id", "", "key id (e.g. index-2026-10)")
	out := fs.String("out", "", "file the seed is written to (mode 0600, must not exist)")
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if *id == "" || *out == "" || fs.NArg() != 0 {
		return usageError{"-id and -out are required; the seed is never printed"}
	}
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return err
	}
	k, err := signing.NewKeyFromSeed(*id, seed)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("seed file: %w (an existing file is never overwritten)", err)
	}
	if _, err := f.WriteString(hex.EncodeToString(seed) + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "id: %s\npublic: %s\nseed: written to %s (mode 0600)\n", k.ID, k.PublicHex(), *out)
	return nil
}

func readKey(path, id string) (*signing.Key, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return signing.ParsePrivateKey(id, string(b))
}

func sign(args []string, stdout io.Writer) error {
	fs := flags("sign")
	keyFile := fs.String("key", "", "seed (or private key) file, hex or base64")
	sha := fs.String("sha256", "", "sign this sha256 (hex) instead of a file's")
	name := fs.String("name", "", "a storage plugin build: the plugin's name (with -version and -platform the build's text is signed, not the sha256 alone)")
	version := fs.String("version", "", "a storage plugin build: its version")
	platform := fs.String("platform", "", "a storage plugin build: its platform, e.g. linux/amd64")
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	build := *name != "" || *version != "" || *platform != ""
	if build && (*name == "" || *version == "" || *platform == "") {
		return usageError{"a storage plugin build needs all of -name, -version and -platform"}
	}
	if *keyFile == "" || (*sha == "") == (fs.NArg() != 1) || fs.NArg() > 1 {
		return usageError{"-key and exactly one of -sha256 or a file"}
	}
	k, err := readKey(*keyFile, "cli")
	if err != nil {
		return err
	}
	digest := strings.ToLower(strings.TrimSpace(*sha))
	if digest == "" {
		b, err := os.ReadFile(fs.Arg(0))
		if err != nil {
			return err
		}
		digest = signing.SHA256Hex(b)
	} else if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
		return usageError{"-sha256 must be 64 hex characters"}
	}
	if build {
		fmt.Fprintf(stdout, "%s\n", k.SignStorageBuild(*name, *version, *platform, digest))
		return nil
	}
	fmt.Fprintf(stdout, "%s\n", k.SignSHA256Hex(digest))
	return nil
}

const maxIndexBytes = 32 << 20

func fetch(ctx context.Context, hc *http.Client, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxIndexBytes+1))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %d: %s", u, resp.StatusCode, strings.TrimSpace(string(b[:min(len(b), 200)])))
	}
	if len(b) > maxIndexBytes {
		return nil, fmt.Errorf("%s is larger than %d MiB", u, maxIndexBytes>>20)
	}
	return b, nil
}

func verifyIndex(args []string, stdout io.Writer, now func() time.Time) error {
	fs := flags("verify-index")
	keyList := fs.String("key", "", "trusted index public key(s), hex or base64, comma separated")
	minSerial := fs.Int64("min-serial", 0, "the highest serial accepted before; a lower one is a rollback")
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if *keyList == "" || fs.NArg() != 1 {
		return usageError{"-key and the index URL are required (the trusted key never comes from the index itself)"}
	}
	var keys []ed25519.PublicKey
	for _, s := range strings.Split(*keyList, ",") {
		k, err := signing.ParsePublicKey(s)
		if err != nil {
			return fmt.Errorf("-key %q: %w", s, err)
		}
		keys = append(keys, k)
	}
	u := fs.Arg(0)
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		return usageError{"the index URL must be http(s)"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	hc := &http.Client{Timeout: 60 * time.Second}
	body, err := fetch(ctx, hc, u)
	if err != nil {
		return err
	}
	sig, err := fetch(ctx, hc, u+".sig")
	if err != nil {
		return err
	}
	sum := signing.SHA256Hex(body)
	if err := signing.Verify(keys, sum, strings.TrimSpace(string(sig))); err != nil {
		return fmt.Errorf("signature: %w", err)
	}
	var doc index.Document
	if err := json.Unmarshal(body, &doc); err != nil {
		return fmt.Errorf("the signed body is not an index: %w", err)
	}
	if doc.Schema != index.SchemaVersion {
		return fmt.Errorf("schema %d; this tool reads schema %d", doc.Schema, index.SchemaVersion)
	}
	exp, err := time.Parse(time.RFC3339, doc.ExpiresAt)
	if err != nil {
		return fmt.Errorf("expires_at %q is not RFC 3339", doc.ExpiresAt)
	}
	if !now().Before(exp) {
		return fmt.Errorf("expired at %s (a frozen index)", doc.ExpiresAt)
	}
	if doc.Serial < *minSerial {
		return fmt.Errorf("serial %d is below %d (a rollback)", doc.Serial, *minSerial)
	}
	versions := 0
	for _, a := range doc.Apps {
		versions += len(a.Versions)
	}
	fmt.Fprintf(stdout, "ok: serial %d, expires %s, %d apps, %d versions, sha256 %s\n", doc.Serial, doc.ExpiresAt, len(doc.Apps), versions, sum)
	return nil
}

func vectors(args []string, stdout io.Writer) error {
	fs := flags("vectors")
	file := fs.String("file", "backend/internal/signing/testdata/vectors.json", "the vectors file")
	if err := fs.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	b, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var doc struct {
		Vectors []struct {
			Name         string `json:"name"`
			SeedHex      string `json:"seed_hex"`
			PublicHex    string `json:"public_hex"`
			MessageHex   string `json:"message_hex"`
			SHA256Hex    string `json:"sha256_hex"`
			SignatureHex string `json:"signature_hex"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return err
	}
	if len(doc.Vectors) == 0 {
		return errors.New("no vectors")
	}
	bad := 0
	for _, v := range doc.Vectors {
		seed, err1 := hex.DecodeString(v.SeedHex)
		msg, err2 := hex.DecodeString(v.MessageHex)
		var problems []string
		if err1 != nil || err2 != nil {
			problems = append(problems, "seed or message is not hex")
		} else if k, err := signing.NewKeyFromSeed(v.Name, seed); err != nil {
			problems = append(problems, err.Error())
		} else {
			if k.PublicHex() != v.PublicHex {
				problems = append(problems, "public key differs")
			}
			if signing.SHA256Hex(msg) != v.SHA256Hex {
				problems = append(problems, "sha256 differs")
			}
			if k.SignBytes(msg) != v.SignatureHex {
				problems = append(problems, "signature differs")
			}
			pub, _ := signing.ParsePublicKey(v.PublicHex)
			if pub == nil || signing.Verify([]ed25519.PublicKey{pub}, v.SHA256Hex, v.SignatureHex) != nil {
				problems = append(problems, "does not verify")
			}
		}
		if len(problems) > 0 {
			bad++
			fmt.Fprintf(stdout, "FAIL %s: %s\n", v.Name, strings.Join(problems, "; "))
		} else {
			fmt.Fprintf(stdout, "ok   %s\n", v.Name)
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d of %d vectors do not match the Go signer", bad, len(doc.Vectors))
	}
	return nil
}
