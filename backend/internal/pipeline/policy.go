package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/index"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// PolicyFacts is what the database says about a resolved submission: the
// app bound to its repository, the app holding its name, whether the name is
// reserved, and every app name (for the near-name warning).
type PolicyFacts struct {
	ByRepo         *store.App
	ByName         *store.App
	Versions       []string // the versions ByRepo already has
	Reserved       bool
	ReservedReason string
	Names          map[string]string // name -> status, every app
}

// LoadPolicyFacts reads the facts for a resolution.
func LoadPolicyFacts(ctx context.Context, q db.Querier, res *domain.Resolution) (PolicyFacts, error) {
	var f PolicyFacts
	if a, err := store.GetAppByRepoID(ctx, q, res.RepoID); err == nil {
		f.ByRepo = &a
		vs, err := store.VersionsOf(ctx, q, a.ID)
		if err != nil {
			return f, err
		}
		for _, v := range vs {
			f.Versions = append(f.Versions, v.Version)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return f, err
	}
	if a, err := store.GetAppByName(ctx, q, res.Name); err == nil {
		f.ByName = &a
	} else if !errors.Is(err, store.ErrNotFound) {
		return f, err
	}
	var err error
	if f.Reserved, f.ReservedReason, err = store.ReservedName(ctx, q, res.Name); err != nil {
		return f, err
	}
	f.Names, err = store.AppNames(ctx, q)
	return f, err
}

type finding struct {
	status  domain.CheckStatus
	code    string
	message string
}

// listingKind is a listing's kind; none is an app (the column's default).
func listingKind(k string) string {
	if k == "" {
		return string(domain.KindApp)
	}
	return k
}

// kindWord names a listing's kind in a sentence.
func kindWord(k string) string {
	switch listingKind(k) {
	case string(domain.KindApp):
		return "an app"
	case string(domain.KindLanguagePack):
		return "a language pack"
	case string(domain.KindStorage):
		return "a storage plugin"
	}
	return fmt.Sprintf("%q", k)
}

// Policy is check 5 of the pipeline, the part that needs the catalog: the
// name (taken, reserved, kept by a known app, close to a listed one), the
// version (valid, new, ordered) and the license. It returns one row: failed
// with the first failure's code, warned when only warnings, else passed;
// every finding is in data.
func Policy(res *domain.Resolution, publisherID string, f PolicyFacts, now time.Time) domain.CheckResult {
	var out []finding
	add := func(s domain.CheckStatus, code, msg string, args ...any) {
		out = append(out, finding{s, code, fmt.Sprintf(msg, args...)})
	}

	// Name.
	switch {
	case f.ByRepo != nil:
		if f.ByRepo.Name != res.Name {
			add(domain.CheckFailed, "name_changed", "This repository is listed as %q; a listed app keeps its name (the manifest says %q).", f.ByRepo.Name, res.Name)
		}
		if f.ByRepo.PublisherID != publisherID {
			add(domain.CheckFailed, "publisher_changed", "This repository is listed under the publisher %q.", f.ByRepo.PublisherID)
		}
		if listingKind(f.ByRepo.Kind) != listingKind(string(res.Kind)) {
			add(domain.CheckFailed, "kind_changed", "This repository is listed as %s; a listing keeps its kind (this submission is %s).",
				kindWord(f.ByRepo.Kind), kindWord(string(res.Kind)))
		}
	case f.ByName != nil:
		if f.ByName.PublisherID == publisherID {
			add(domain.CheckFailed, "repo_changed", "The app %q is bound to another repository (GitHub id %d); an app keeps its repository.", res.Name, f.ByName.RepoID)
		} else {
			add(domain.CheckFailed, "name_taken", "The name %q belongs to another app.", res.Name)
		}
	case f.Reserved:
		add(domain.CheckFailed, "name_reserved", "The name %q is reserved (%s).", res.Name, f.ReservedReason)
	default:
		var near []string
		for n, status := range f.Names {
			if status != "revoked" && n != res.Name && oneEditApart(n, res.Name) {
				near = append(near, n)
			}
		}
		sort.Strings(near)
		if len(near) > 0 {
			add(domain.CheckWarned, "name_similar", "The name %q is one edit away from %s; the reviewer checks it is not confusable.", res.Name, strings.Join(near, ", "))
		}
	}

	// Version.
	switch {
	case !index.ValidVersion(res.Version):
		add(domain.CheckFailed, "version_invalid", "The version %q is not a semantic version.", res.Version)
	default:
		newest := ""
		for _, v := range f.Versions {
			if v == res.Version || index.CompareVersions(v, res.Version) == 0 {
				add(domain.CheckFailed, "version_exists", "The version %s is already listed.", res.Version)
				newest = ""
				break
			}
			if newest == "" || index.CompareVersions(v, newest) > 0 {
				newest = v
			}
		}
		if newest != "" && index.CompareVersions(res.Version, newest) < 0 {
			add(domain.CheckWarned, "version_backport", "The version %s is lower than the newest listed (%s): a backport.", res.Version, newest)
		}
	}

	// License (GitHub's detection; the reviewer checks it is real).
	switch strings.ToLower(strings.TrimSpace(res.License)) {
	case "", "noassertion", "other":
		add(domain.CheckFailed, "license_missing", "No open-source license was detected in the repository.")
	}

	c := domain.CheckResult{Kind: domain.CheckPolicy, Status: domain.CheckPassed, StartedAt: now}
	end := now
	c.EndedAt = &end
	type item struct {
		Status  domain.CheckStatus `json:"status"`
		Code    string             `json:"code"`
		Message string             `json:"message"`
	}
	var items []item
	for _, x := range out {
		items = append(items, item{x.status, x.code, x.message})
		if x.status == domain.CheckFailed && c.Status != domain.CheckFailed {
			c.Status, c.Code, c.Message = domain.CheckFailed, x.code, x.message
		}
		if x.status == domain.CheckWarned && c.Status == domain.CheckPassed {
			c.Status, c.Code, c.Message = domain.CheckWarned, x.code, x.message
		}
	}
	if c.Status == domain.CheckPassed {
		c.Message = "Name, version and license are in order."
	}
	if len(items) > 0 {
		c.Data, _ = json.Marshal(map[string]any{"findings": items})
	}
	return c
}

// oneEditApart reports a Levenshtein distance of exactly one (one letter
// inserted, deleted or replaced) or two adjacent letters swapped.
func oneEditApart(a, b string) bool {
	if a == b {
		return false
	}
	ra, rb := []rune(a), []rune(b)
	la, lb := len(ra), len(rb)
	if la-lb > 1 || lb-la > 1 {
		return false
	}
	if la == lb {
		var diff []int
		for i := range ra {
			if ra[i] != rb[i] {
				diff = append(diff, i)
				if len(diff) > 2 {
					return false
				}
			}
		}
		if len(diff) == 1 {
			return true
		}
		return len(diff) == 2 && diff[1] == diff[0]+1 && ra[diff[0]] == rb[diff[1]] && ra[diff[1]] == rb[diff[0]]
	}
	if la < lb {
		ra, rb = rb, ra
	}
	// ra is one longer: skipping one rune of ra must give rb.
	i, j, skipped := 0, 0, false
	for i < len(ra) && j < len(rb) {
		if ra[i] == rb[j] {
			i++
			j++
			continue
		}
		if skipped {
			return false
		}
		skipped = true
		i++
	}
	return true
}
