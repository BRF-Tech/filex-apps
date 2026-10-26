package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The fixture directory (e2e/fixtures):
//
//	users.json                       GitHub accounts
//	repos.json                       repositories: owner, numeric ids, license,
//	                                 who holds which role, the tags, a template,
//	                                 whether the server token sees it
//	templates/<t>/files/...          the repository's files at each tag
//	templates/<t>/release/<asset>    the tag's release assets; a DIRECTORY
//	                                 named x.zip is zipped (fixed timestamps,
//	                                 sorted names, so the bytes never change)
//
// Text files are templates: ${OWNER} ${REPO} ${TAG} ${VERSION}, the repo's
// own "vars", and ${ASSET_SHA256:<asset>} (the asset's hash, so a manifest
// pins the bundle it ships with). A file "<name>.gen" generates <name>:
// "png <width> <height> #rrggbb" is a solid PNG. No binary sits in the
// repository.

// GitHubUser is a GitHub account the OAuth page offers.
type GitHubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type usersFile struct {
	GitHub []GitHubUser `json:"github"`
}

// RepoSpec is one entry of repos.json.
type RepoSpec struct {
	Owner     string            `json:"owner"`
	OwnerID   int64             `json:"owner_id"`
	OwnerType string            `json:"owner_type"` // User (default) or Organization
	Repo      string            `json:"repo"`
	ID        int64             `json:"id"`
	Private   bool              `json:"private"`
	Archived  bool              `json:"archived"`
	License   string            `json:"license"` // SPDX id, "" = none detected
	Roles     map[string]string `json:"roles"`   // login -> admin|maintain|write|triage|read
	// ServerToken: the store's admin-repository token (MOCK_GITHUB_ADMIN_TOKEN)
	// sees this repository. Only the administration repository has it.
	ServerToken bool     `json:"server_token"`
	Template    string   `json:"template"`
	Tags        []string `json:"tags"`
	// Lightweight lists tags that are NOT annotated (GitHub answers a
	// commit object for them instead of a tag object).
	Lightweight []string          `json:"lightweight"`
	Vars        map[string]string `json:"vars"`
}

// Tag is one tag of a repository, fully materialised.
type Tag struct {
	Name      string
	Commit    string // 40 hex, derived from repo + tag
	TagObject string // annotated tags only
	Files     map[string][]byte
	Assets    map[string][]byte
	AssetList []string
}

// Repo is a repository with its tags.
type Repo struct {
	RepoSpec
	FullName string
	TagList  []*Tag
	ByName   map[string]*Tag
	ByCommit map[string]*Tag
}

// Fixtures is everything the GitHub side serves.
type Fixtures struct {
	GitHubUsers []GitHubUser
	repos       map[string]*Repo // lower-case "owner/repo"
	byID        map[int64]*Repo
}

func loadFixtures(dir string) (*Fixtures, error) {
	var uf usersFile
	if err := readJSON(filepath.Join(dir, "users.json"), &uf); err != nil {
		return nil, err
	}
	var specs []RepoSpec
	if err := readJSON(filepath.Join(dir, "repos.json"), &specs); err != nil {
		return nil, err
	}
	fx := &Fixtures{GitHubUsers: uf.GitHub, repos: map[string]*Repo{}, byID: map[int64]*Repo{}}
	for _, s := range specs {
		r, err := buildRepo(dir, s)
		if err != nil {
			return nil, fmt.Errorf("repos.json %s/%s: %w", s.Owner, s.Repo, err)
		}
		fx.repos[strings.ToLower(r.FullName)] = r
		fx.byID[r.ID] = r
	}
	return fx, nil
}

func buildRepo(dir string, s RepoSpec) (*Repo, error) {
	if s.OwnerType == "" {
		s.OwnerType = "User"
	}
	r := &Repo{RepoSpec: s, FullName: s.Owner + "/" + s.Repo, ByName: map[string]*Tag{}, ByCommit: map[string]*Tag{}}
	light := map[string]bool{}
	for _, t := range s.Lightweight {
		light[t] = true
	}
	tdir := filepath.Join(dir, "templates", s.Template)
	for _, name := range s.Tags {
		vars := map[string]string{
			"OWNER": s.Owner, "REPO": s.Repo, "TAG": name,
			"VERSION":  strings.TrimPrefix(name, "v"),
			"OWNER_ID": strconv.FormatInt(s.OwnerID, 10), "REPO_ID": strconv.FormatInt(s.ID, 10),
		}
		for k, v := range s.Vars {
			vars[k] = v
		}
		t := &Tag{Name: name, Commit: fakeSHA("commit", r.FullName, name), Files: map[string][]byte{}, Assets: map[string][]byte{}}
		if !light[name] {
			t.TagObject = fakeSHA("tag", r.FullName, name)
		}
		assets, err := readTree(filepath.Join(tdir, "release"), true)
		if err != nil {
			return nil, err
		}
		for n, b := range assets {
			t.Assets[n] = substitute(b, vars, nil)
			t.AssetList = append(t.AssetList, n)
		}
		sort.Strings(t.AssetList)
		sums := map[string]string{}
		for n, b := range t.Assets {
			h := sha256.Sum256(b)
			sums[n] = hex.EncodeToString(h[:])
		}
		files, err := readTree(filepath.Join(tdir, "files"), false)
		if err != nil {
			return nil, err
		}
		for n, b := range files {
			t.Files[n] = substitute(b, vars, sums)
		}
		r.TagList = append(r.TagList, t)
		r.ByName[name] = t
		r.ByCommit[t.Commit] = t
	}
	return r, nil
}

// readTree reads every file under root, relative slash paths. With zipDirs, a
// directory whose name ends in .zip becomes one deterministic zip.
func readTree(root string, zipDirs bool) (map[string][]byte, error) {
	out := map[string][]byte{}
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return out, nil
	}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if zipDirs && rel != "." && strings.HasSuffix(rel, ".zip") {
				b, zerr := zipDir(p)
				if zerr != nil {
					return zerr
				}
				out[rel] = b
				return filepath.SkipDir
			}
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.HasSuffix(rel, ".gen") {
			gb, gerr := generate(strings.TrimSpace(string(b)))
			if gerr != nil {
				return fmt.Errorf("%s: %w", rel, gerr)
			}
			out[strings.TrimSuffix(rel, ".gen")] = gb
			return nil
		}
		out[rel] = b
		return nil
	})
	return out, err
}

var fixedTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func zipDir(dir string) ([]byte, error) {
	files, err := readTree(dir, false)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range names {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: n, Method: zip.Deflate, Modified: fixedTime})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(files[n]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// generate makes a binary fixture from a one-line recipe.
func generate(recipe string) ([]byte, error) {
	f := strings.Fields(recipe)
	if len(f) == 4 && f[0] == "png" {
		w, err1 := strconv.Atoi(f[1])
		h, err2 := strconv.Atoi(f[2])
		c, err3 := parseColor(f[3])
		if err1 != nil || err2 != nil || err3 != nil || w <= 0 || h <= 0 || w > 4096 || h > 4096 {
			return nil, fmt.Errorf("bad png recipe %q", recipe)
		}
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				img.Set(x, y, c)
			}
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	return nil, fmt.Errorf("unknown recipe %q", recipe)
}

func parseColor(s string) (color.NRGBA, error) {
	s = strings.TrimPrefix(s, "#")
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 3 {
		return color.NRGBA{}, fmt.Errorf("bad colour %q", s)
	}
	return color.NRGBA{R: b[0], G: b[1], B: b[2], A: 255}, nil
}

var placeholder = regexp.MustCompile(`\$\{([A-Z0-9_]+)(?::([^}]+))?\}`)

// substitute fills ${VAR} and ${ASSET_SHA256:name} in UTF-8 text. Binary
// files and unknown names are left alone (an unknown name stays visible in
// the served bytes, which is how a broken fixture shows itself).
func substitute(b []byte, vars, sums map[string]string) []byte {
	if !utf8.Valid(b) || !bytes.Contains(b, []byte("${")) {
		return b
	}
	return placeholder.ReplaceAllFunc(b, func(m []byte) []byte {
		g := placeholder.FindSubmatch(m)
		name, arg := string(g[1]), string(g[2])
		if name == "ASSET_SHA256" {
			if v, ok := sums[arg]; ok {
				return []byte(v)
			}
			return m
		}
		if v, ok := vars[name]; ok {
			return []byte(v)
		}
		return m
	})
}

// fakeSHA is a stable 40-hex id: the same repo and tag give the same commit
// in every run and in every process of the stack.
func fakeSHA(kind, repo, tag string) string {
	h := sha1.Sum([]byte(kind + "\x00" + strings.ToLower(repo) + "\x00" + tag))
	return hex.EncodeToString(h[:])
}

// blobSHA is git's id for a file's bytes (what the contents API calls sha).
func blobSHA(b []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(b))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func (fx *Fixtures) repo(owner, name string) *Repo {
	return fx.repos[strings.ToLower(owner+"/"+name)]
}

func (fx *Fixtures) githubUser(login string) *GitHubUser {
	for i := range fx.GitHubUsers {
		if strings.EqualFold(fx.GitHubUsers[i].Login, login) {
			return &fx.GitHubUsers[i]
		}
	}
	return nil
}

// resolve finds a tag by name, by "refs/tags/<name>", or by commit; the
// default branch ("main" or "refs/heads/main") is the last tag's commit.
func (r *Repo) resolve(ref string) *Tag {
	ref = strings.TrimPrefix(ref, "refs/tags/")
	if t, ok := r.ByName[ref]; ok {
		return t
	}
	if t, ok := r.ByCommit[strings.ToLower(ref)]; ok {
		return t
	}
	if (ref == "main" || ref == "refs/heads/main" || ref == "") && len(r.TagList) > 0 {
		return r.TagList[len(r.TagList)-1]
	}
	return nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
