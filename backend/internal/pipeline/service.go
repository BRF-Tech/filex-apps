package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brf-tech/filex-apps/backend/internal/audit"
	"github.com/brf-tech/filex-apps/backend/internal/consent"
	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/filetypes"
	"github.com/brf-tech/filex-apps/backend/internal/filexrange"
	ghapi "github.com/brf-tech/filex-apps/backend/internal/github"
	"github.com/brf-tech/filex-apps/backend/internal/index"
	"github.com/brf-tech/filex-apps/backend/internal/notify"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// RefusalKind says how a refusal maps to HTTP.
type RefusalKind int

// Refusal kinds.
const (
	Invalid      RefusalKind = iota + 1 // 400
	Forbidden                           // 403
	NotFound                            // 404
	Conflict                            // 409
	Unavailable                         // 503
	Gone                                // 410 (a spent or expired install link)
	Unauthorized                        // 401 (a connected filex's request that does not hold)
)

// Refusal is a request the rules refuse, with a machine code and a sentence.
type Refusal struct {
	Kind    RefusalKind
	Code    string
	Message string
	// Submission is the open submission a submission_open refusal names.
	Submission *store.Submission
}

func (r *Refusal) Error() string { return r.Code + ": " + r.Message }

func refuse(kind RefusalKind, code, format string, args ...any) *Refusal {
	return &Refusal{Kind: kind, Code: code, Message: fmt.Sprintf(format, args...)}
}

// RepoAccess is what the store keeps of GitHub's answer about a repository
// at submission time.
type RepoAccess struct {
	ID        int64
	Owner     string
	Name      string
	OwnerID   int64
	OwnerType string
}

// RepoAccessChecker is check 1 (repo_access) on its own, with the
// submitter's token: internal/resolve's Resolver.RepoAccess. It returns the
// repository whenever GitHub showed it; err is only for a failure worth
// retrying.
type RepoAccessChecker interface {
	RepoAccess(ctx context.Context, repo, userToken string) (*ghapi.Repo, domain.CheckResult, error)
}

// Actor is who does something, for the rules and the audit log.
type Actor struct {
	AccountID int64
	Role      string // an audit role: publisher, reviewer, admin
	Admin     bool
	IP        string
}

// Service is the submission lifecycle and the catalog administration.
type Service struct {
	Pool  *pgxpool.Pool
	Repos RepoAccessChecker
	Keys  *index.Keyring
	Index *index.Releaser
	Kick  func()
	Log   *slog.Logger
	// Profiles reads a publisher's GitHub profile and mirrors its avatar
	// when the publisher becomes active (nil: no profile is kept).
	Profiles ProfileReader
}

func (s *Service) kick() {
	if s.Kick != nil {
		s.Kick()
	}
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

var (
	repoOwnerRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	repoNameRe  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// ParseRepo reads "owner/name" or a https://github.com/owner/name address.
func ParseRepo(s string) (owner, name string, ok bool) {
	s = strings.TrimSpace(s)
	for _, p := range []string{"https://github.com/", "http://github.com/", "github.com/"} {
		if strings.HasPrefix(strings.ToLower(s), p) {
			s = s[len(p):]
			break
		}
	}
	s = strings.TrimSuffix(strings.TrimSuffix(s, "/"), ".git")
	owner, name, found := strings.Cut(s, "/")
	if !found || !repoOwnerRe.MatchString(owner) || !repoNameRe.MatchString(name) || name == "." || name == ".." {
		return "", "", false
	}
	return owner, name, true
}

// ValidRef is a tag name the store will look up (the resolver decides it is
// a tag).
func ValidRef(ref string) bool {
	if ref == "" || len(ref) > 255 || strings.HasPrefix(ref, "-") || strings.Contains(ref, "..") {
		return false
	}
	for _, r := range ref {
		if r <= ' ' || r == 0x7f || strings.ContainsRune("~^:?*[\\", r) {
			return false
		}
	}
	return true
}

// SubmitRequest is a publisher's submission.
type SubmitRequest struct {
	Actor     Actor
	Token     string // the submitter's GitHub token (used once, here)
	Publisher string
	Repo      string
	Ref       string
	Note      string
	// Kind is what is submitted: "app" (the default: an app or a language
	// pack) or "storage" (a storage plugin, listed from its release's
	// filex-storage.json).
	Kind string
}

// Submit checks synchronously that the submitter may list the repository
// (membership, publisher active, repository public and not archived, GitHub
// says admin or maintain, owner bound to the publisher) and queues the
// pipeline. Everything else runs in the background.
func (s *Service) Submit(ctx context.Context, in SubmitRequest) (store.Submission, error) {
	owner, name, ok := ParseRepo(in.Repo)
	if !ok {
		return store.Submission{}, refuse(Invalid, "invalid_field", "repo must be owner/name or a https://github.com/owner/name address.")
	}
	if !ValidRef(in.Ref) {
		return store.Submission{}, refuse(Invalid, "invalid_field", "ref must be a tag name.")
	}
	if in.Kind == "" {
		in.Kind = string(domain.SubmitApp)
	}
	if !domain.ValidSubmissionKind(in.Kind) {
		return store.Submission{}, refuse(Invalid, "invalid_field", "kind must be app or storage.")
	}
	repo := owner + "/" + name
	pub, err := s.memberPublisher(ctx, in.Publisher, in.Actor.AccountID)
	if err != nil {
		return store.Submission{}, err
	}
	if app, version, err := store.VersionAtRepoRef(ctx, s.Pool, repo, in.Ref); err != nil {
		return store.Submission{}, err
	} else if version != "" {
		return store.Submission{}, refuse(Conflict, "version_exists", "%s at %s is already listed as %s %s.", repo, in.Ref, app, version)
	}
	if id, err := store.FindOpenSubmission(ctx, s.Pool, repo, in.Ref); err == nil {
		return store.Submission{}, s.openRefusal(ctx, id)
	}
	access, check, err := s.checkRepo(ctx, in.Token, pub, owner, name)
	if err != nil {
		return store.Submission{}, err
	}

	var id int64
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		id, err = store.InsertSubmissionKind(ctx, tx, pub.ID, in.Actor.AccountID, access.Owner+"/"+access.Name, in.Ref, in.Note, in.Kind)
		if err != nil {
			return err
		}
		if err := store.InsertChecks(ctx, tx, id, 1, []domain.CheckResult{check}); err != nil {
			return err
		}
		if _, err := store.Enqueue(ctx, tx, store.JobResolve, SubmissionJob{SubmissionID: id, Attempt: 1}, time.Time{}); err != nil {
			return err
		}
		_, err = audit.Append(ctx, tx, s.entry(in.Actor, "submission.create", "submission", id,
			map[string]any{"publisher": pub.ID, "repo": access.Owner + "/" + access.Name, "repo_id": access.ID, "ref": in.Ref, "kind": in.Kind}))
		return err
	})
	var open *store.OpenSubmissionError
	if errors.As(err, &open) {
		return store.Submission{}, s.openRefusal(ctx, open.ID)
	}
	if err != nil {
		return store.Submission{}, err
	}
	s.kick()
	return store.GetSubmission(ctx, s.Pool, id)
}

func (s *Service) openRefusal(ctx context.Context, id int64) error {
	r := refuse(Conflict, "submission_open", "This repository at this tag is already in the pipeline.")
	if sub, err := store.GetSubmission(ctx, s.Pool, id); err == nil {
		r.Submission = &sub
	}
	return r
}

// memberPublisher returns the publisher when the account is a member and it
// is active.
func (s *Service) memberPublisher(ctx context.Context, publisherID string, account int64) (store.Publisher, error) {
	role, err := store.MemberRole(ctx, s.Pool, publisherID, account)
	if err != nil {
		return store.Publisher{}, err
	}
	if role == "" {
		return store.Publisher{}, refuse(Forbidden, "not_a_member", "You are not a member of the publisher %q.", publisherID)
	}
	pub, err := store.GetPublisher(ctx, s.Pool, publisherID)
	if err != nil {
		return store.Publisher{}, err
	}
	if pub.Status != "active" {
		return store.Publisher{}, refuse(Forbidden, "publisher_not_active", "The publisher %q is %s; an administrator activates publishers.", pub.ID, pub.Status)
	}
	return pub, nil
}

// checkRepo is check 1 (repo_access), synchronously, with the person's
// token: the resolver's own check (public, not archived, admin or maintain),
// then the owner the publisher is bound to.
func (s *Service) checkRepo(ctx context.Context, token string, pub store.Publisher, owner, name string) (RepoAccess, domain.CheckResult, error) {
	if s.Repos == nil || token == "" {
		return RepoAccess{}, domain.CheckResult{}, refuse(Forbidden, "github_required", "Sign in with GitHub to submit.")
	}
	repo, check, err := s.Repos.RepoAccess(ctx, owner+"/"+name, token)
	if err != nil {
		return RepoAccess{}, domain.CheckResult{}, refuse(Unavailable, "github_unavailable", "GitHub could not be asked: %v", err)
	}
	if check.Status == domain.CheckFailed {
		code := check.Code
		if code == "" {
			code = "repo_access_failed"
		}
		// "GitHub did not answer" is the store's own trouble, not the
		// submitter's: it answers 503, so a client knows to try again
		// (BRF-Tech/filex#118).
		if code == "github_unavailable" {
			return RepoAccess{}, domain.CheckResult{}, refuse(Unavailable, code, "%s", check.Message)
		}
		return RepoAccess{}, domain.CheckResult{}, refuse(Forbidden, code, "%s", check.Message)
	}
	if repo == nil {
		return RepoAccess{}, domain.CheckResult{}, refuse(Unavailable, "github_unavailable", "GitHub did not show %s/%s.", owner, name)
	}
	if repo.Owner.ID != pub.GitHubOwnerID {
		return RepoAccess{}, domain.CheckResult{}, refuse(Forbidden, "owner_mismatch",
			"%s belongs to %s, not to the publisher's GitHub owner %s.", repo.FullName, repo.Owner.Login, pub.GitHubOwner)
	}
	if check.Kind == "" {
		check.Kind = domain.CheckRepoAccess
	}
	if check.StartedAt.IsZero() {
		check.StartedAt = time.Now()
	}
	repoName := repo.Name
	if repoName == "" {
		repoName = name
	}
	return RepoAccess{ID: repo.ID, Owner: repo.Owner.Login, Name: repoName, OwnerID: repo.Owner.ID, OwnerType: repo.Owner.Type}, check, nil
}

// CanSee reports whether an account may see a submission: a reviewer sees
// all, a publisher member its publishers'.
func (s *Service) CanSee(ctx context.Context, sub store.Submission, account int64, reviewer bool) (bool, error) {
	if reviewer {
		return true, nil
	}
	role, err := store.MemberRole(ctx, s.Pool, sub.PublisherID, account)
	return role != "", err
}

// Withdraw ends a submission that is not decided yet (a member of its
// publisher).
func (s *Service) Withdraw(ctx context.Context, id int64, a Actor) (store.Submission, error) {
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		sub, err := s.lockVisible(ctx, tx, id, a, false)
		if err != nil {
			return err
		}
		if !IsOpen(domain.SubmissionStatus(sub.Status)) {
			return refuse(Conflict, "not_pending", "The submission is %s.", sub.Status)
		}
		if _, err := store.Withdraw(ctx, tx, id); err != nil {
			return err
		}
		if _, err := store.CancelSubmissionJobs(ctx, tx, id); err != nil {
			return err
		}
		_, err = audit.Append(ctx, tx, s.entry(a, "submission.withdraw", "submission", id, map[string]any{"from": sub.Status}))
		return err
	})
	if err != nil {
		return store.Submission{}, err
	}
	return store.GetSubmission(ctx, s.Pool, id)
}

// Retry starts a new attempt of a failed submission: the repository check
// again with the person's token, then the whole pipeline (the tag is
// resolved again, so a moved tag is caught).
func (s *Service) Retry(ctx context.Context, id int64, a Actor, token string) (store.Submission, error) {
	sub, err := store.GetSubmission(ctx, s.Pool, id)
	if errors.Is(err, store.ErrNotFound) {
		return store.Submission{}, refuse(NotFound, "not_found", "No such submission.")
	}
	if err != nil {
		return store.Submission{}, err
	}
	pub, err := s.memberPublisher(ctx, sub.PublisherID, a.AccountID)
	if err != nil {
		var r *Refusal
		if errors.As(err, &r) && r.Code == "not_a_member" {
			return store.Submission{}, refuse(NotFound, "not_found", "No such submission.")
		}
		return store.Submission{}, err
	}
	if sub.Status != string(domain.SubFailed) {
		return store.Submission{}, refuse(Conflict, "not_failed", "Only a failed submission can be retried; this one is %s.", sub.Status)
	}
	owner, name, _ := ParseRepo(sub.Repo)
	_, check, err := s.checkRepo(ctx, token, pub, owner, name)
	if err != nil {
		return store.Submission{}, err
	}
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		attempt, err := store.Retry(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := store.InsertChecks(ctx, tx, id, attempt, []domain.CheckResult{check}); err != nil {
			return err
		}
		if _, err := store.Enqueue(ctx, tx, store.JobResolve, SubmissionJob{SubmissionID: id, Attempt: attempt}, time.Time{}); err != nil {
			return err
		}
		_, err = audit.Append(ctx, tx, s.entry(a, "submission.retry", "submission", id, map[string]any{"attempt": attempt}))
		return err
	})
	var open *store.OpenSubmissionError
	switch {
	case errors.As(err, &open):
		return store.Submission{}, s.openRefusal(ctx, open.ID)
	case errors.Is(err, store.ErrNotFound):
		return store.Submission{}, refuse(Conflict, "not_failed", "Only a failed submission can be retried.")
	case err != nil:
		return store.Submission{}, err
	}
	s.kick()
	return store.GetSubmission(ctx, s.Pool, id)
}

// lockVisible locks a submission the actor may act on (a member, or a
// reviewer when reviewer is true); others get not_found.
func (s *Service) lockVisible(ctx context.Context, tx pgx.Tx, id int64, a Actor, reviewer bool) (store.Submission, error) {
	sub, err := store.LockSubmission(ctx, tx, id)
	if errors.Is(err, store.ErrNotFound) {
		return sub, refuse(NotFound, "not_found", "No such submission.")
	}
	if err != nil {
		return sub, err
	}
	if !reviewer {
		role, err := store.MemberRole(ctx, tx, sub.PublisherID, a.AccountID)
		if err != nil {
			return sub, err
		}
		if role == "" {
			return sub, refuse(NotFound, "not_found", "No such submission.")
		}
	}
	return sub, nil
}

// Decision is a reviewer's call.
type Decision struct {
	Actor    Actor
	Note     string
	Security bool
}

// ApproveResult is what an approval wrote.
type ApproveResult struct {
	Submission store.Submission
	App        store.App
	Version    store.Version
	Serial     int64 // the index serial that carries it (0 if publishing is pending)
}

// Approve writes the version (signed by the artifact key), the app on a
// first approval and the listing from the consent file, the review and the
// audit row, in one transaction; then publishes the index (serial + 1). A
// publish_index job queued in the same transaction is the safety net when
// the publish right after fails.
func (s *Service) Approve(ctx context.Context, id int64, d Decision) (ApproveResult, error) {
	if s.Keys == nil || s.Keys.Artifact == nil {
		return ApproveResult{}, refuse(Unavailable, "signing_unavailable", "No artifact key is configured; nothing can be approved.")
	}
	var out ApproveResult
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		sub, err := s.lockVisible(ctx, tx, id, d.Actor, true)
		if err != nil {
			return err
		}
		switch domain.SubmissionStatus(sub.Status) {
		case domain.SubNeedsReview:
		case domain.SubApproved, domain.SubRejected, domain.SubWithdrawn:
			return refuse(Conflict, "already_decided", "The submission is %s.", sub.Status)
		default:
			return refuse(Conflict, "not_pending", "The submission is %s, not waiting for review.", sub.Status)
		}
		checks, err := store.ChecksOf(ctx, tx, sub.ID, sub.Attempt)
		if err != nil {
			return err
		}
		if ok, why := ReadyForApproval(checks); !ok {
			return refuse(Conflict, "checks_not_passed", "%s", why)
		}
		role, err := store.MemberRole(ctx, tx, sub.PublisherID, d.Actor.AccountID)
		if err != nil {
			return err
		}
		selfReview := role != ""
		if selfReview && !d.Actor.Admin {
			return refuse(Forbidden, "own_publisher", "A reviewer does not approve a submission of their own publisher.")
		}
		pub, err := store.GetPublisher(ctx, tx, sub.PublisherID)
		if err != nil {
			return err
		}
		if pub.Status != "active" {
			return refuse(Conflict, "publisher_not_active", "The publisher %q is %s.", pub.ID, pub.Status)
		}
		op, err := store.OperatorListing(ctx, tx, sub.ID)
		if err != nil {
			return err
		}
		operator := op != nil
		if operator && (!pub.Official || !pub.Verified) {
			return refuse(Conflict, "publisher_not_official", "This is an operator import and the publisher %q is no longer official and verified.", pub.ID)
		}
		var res domain.Resolution
		if err := json.Unmarshal(sub.Resolution, &res); err != nil || res.Name == "" {
			return refuse(Conflict, "resolution_unreadable", "The submission has no readable resolution.")
		}

		app, err := s.writeApp(ctx, tx, sub, &res)
		if err != nil {
			return err
		}
		v, err := s.writeVersion(ctx, tx, sub, &res, app, checks, d.Security, conversionsOrigin(&res, op))
		if err != nil {
			return err
		}
		if _, err := store.InsertReview(ctx, tx, store.Review{SubmissionID: sub.ID, ReviewerID: d.Actor.AccountID,
			Decision: "approve", Note: d.Note, Security: d.Security}); err != nil {
			return err
		}
		if err := store.Decide(ctx, tx, sub.ID, string(domain.SubApproved), d.Actor.AccountID, &app.ID); err != nil {
			return err
		}
		data := map[string]any{
			"app": app.Name, "version": v.Version, "commit": v.Commit, "manifest_sha256": v.ManifestSHA256,
			"security": d.Security, "self_review": selfReview}
		if operator {
			data["operator_import"] = true
			data["consent"] = OperatorConsent
		}
		if _, err := audit.Append(ctx, tx, s.entry(d.Actor, "submission.approve", "submission", sub.ID, data)); err != nil {
			return err
		}
		if !operator {
			sub.Name, sub.Version = app.Name, v.Version
			if err := notifyDecision(ctx, tx, notify.KindApproved, sub, d); err != nil {
				return err
			}
		}
		actor := d.Actor.AccountID
		if _, err := store.Enqueue(ctx, tx, store.JobPublishIndex, PublishJob{Reason: index.ReasonApprove, Actor: &actor}, time.Now().Add(30*time.Second)); err != nil {
			return err
		}
		out.App, out.Version = app, v
		return nil
	})
	if err != nil {
		return ApproveResult{}, err
	}
	out.Serial = s.publish(ctx, index.ReasonApprove, d.Actor.AccountID)
	out.Submission, err = store.GetSubmission(ctx, s.Pool, id)
	return out, err
}

// publish publishes now and returns the serial that carries the change
// (0 when it could not; the queued job retries).
func (s *Service) publish(ctx context.Context, reason string, actor int64) int64 {
	defer s.kick()
	if s.Index == nil {
		return 0
	}
	res, err := s.Index.Publish(ctx, reason, &actor, false)
	if err != nil {
		s.log().Error("index publish after "+reason, "err", err)
		if _, qerr := store.Enqueue(context.WithoutCancel(ctx), s.Pool, store.JobPublishIndex, PublishJob{Reason: reason, Actor: &actor}, time.Now().Add(10*time.Second)); qerr != nil {
			s.log().Error("queue index publish", "err", qerr)
		}
		return 0
	}
	return res.Release.Serial
}

// writeApp creates the app at a first approval or refreshes its listing.
func (s *Service) writeApp(ctx context.Context, tx pgx.Tx, sub store.Submission, res *domain.Resolution) (store.App, error) {
	cats, err := knownCategories(ctx, tx, res.Consent.Categories)
	if err != nil {
		return store.App{}, err
	}
	icon, shots, err := listingMedia(ctx, tx, res.Media)
	if err != nil {
		return store.App{}, err
	}
	a := store.App{
		Name: res.Name, Kind: listingKind(string(res.Kind)), PublisherID: sub.PublisherID, RepoOwner: res.RepoOwner, RepoName: res.RepoName,
		RepoID: res.RepoID, Homepage: res.Homepage, License: res.License, Categories: cats,
		Label: ManifestLabel(res.Manifest, res.Name), Summary: map[string]string(res.Consent.Summary),
		DescriptionMD: res.Description, IconSHA256: icon,
	}
	existing, err := store.GetAppByRepoID(ctx, tx, res.RepoID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		a.ID, err = store.InsertApp(ctx, tx, a)
		if errors.Is(err, store.ErrConflict) {
			return store.App{}, refuse(Conflict, "name_taken", "The name %q was taken by another app meanwhile.", res.Name)
		}
		if err != nil {
			return store.App{}, err
		}
	case err != nil:
		return store.App{}, err
	default:
		if existing.Name != res.Name {
			return store.App{}, refuse(Conflict, "name_changed", "This repository is listed as %q.", existing.Name)
		}
		if existing.PublisherID != sub.PublisherID {
			return store.App{}, refuse(Conflict, "publisher_changed", "This repository is listed under %q.", existing.PublisherID)
		}
		// ⚠ A listing keeps its kind: its versions, its install intents and
		// filex's review of them all read the kind from the listing, so an app
		// turned storage plugin (or back) would mix the two under one name.
		// The store refuses it too (UpdateAppListing), should this ever be
		// skipped.
		if listingKind(existing.Kind) != a.Kind {
			return store.App{}, refuse(Conflict, "kind_changed", "This repository is listed as %s; a listing keeps its kind.", kindWord(existing.Kind))
		}
		a.ID = existing.ID
		if err := store.UpdateAppListing(ctx, tx, a); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return store.App{}, refuse(Conflict, "kind_changed", "This repository is listed as another kind of entry; a listing keeps its kind.")
			}
			return store.App{}, err
		}
	}
	if err := store.ReplaceAppMedia(ctx, tx, a.ID, shots); err != nil {
		return store.App{}, err
	}
	return store.GetAppByID(ctx, tx, a.ID)
}

// buildSigner signs a storage plugin build (signing.Key.SignStorageBuild).
type buildSigner interface {
	SignStorageBuild(name, version, platform, sha string) string
}

// signBuilds is a storage plugin's builds as a version keeps them: EVERY
// platform the feed names signed with the artifact key - windows and darwin
// as much as the required linux pair - over the build's name, version,
// platform and lower-case sha256 (signing.StorageBuildPayload).
//
// ⚠ Not over the sha256 alone (sec055 S5): such a signature vouched for the
// bytes and nothing else, so a filex that trusts the artifact key took a
// build the store once signed - an old, withdrawn version, another plugin's,
// another platform's - as "signed" on every install path. The build's own
// text names what the store reviewed, and filex rebuilds it from the
// install link it is installing (plugin.VerifyBuild).
func signBuilds(name, version string, bins map[string]domain.Artifact, key buildSigner) map[string]store.Artifact {
	out := make(map[string]store.Artifact, len(bins))
	for plat, b := range bins {
		sum := strings.ToLower(b.SHA256)
		out[plat] = store.Artifact{URL: b.URL, SHA256: sum, Size: b.Size, Sig: key.SignStorageBuild(name, version, plat, sum)}
	}
	return out
}

func (s *Service) writeVersion(ctx context.Context, tx pgx.Tx, sub store.Submission, res *domain.Resolution, app store.App,
	checks []domain.CheckResult, security bool, origin string) (store.Version, error) {
	key := s.Keys.Artifact
	v := store.Version{
		AppID: app.ID, SubmissionID: sub.ID, Version: res.Version, Ref: res.Ref, Commit: strings.ToLower(res.CommitSHA),
		FilexRange: canonicalRange(res.FilexRange), ManifestURL: res.ManifestURL, ManifestSHA256: strings.ToLower(res.ManifestSHA256),
		ManifestSig: key.SignSHA256Hex(res.ManifestSHA256), Manifest: res.Manifest,
		Permissions: append([]string{}, res.Permissions...), Engines: ManifestEngines(res.Manifest),
		NotesURL: "https://github.com/" + res.RepoOwner + "/" + res.RepoName + "/releases/tag/" + url.PathEscape(res.Ref),
		Security: security, ArtifactKeyID: key.ID,
	}
	if res.Wasm != nil {
		v.Wasm = &store.Artifact{URL: res.Wasm.URL, SHA256: strings.ToLower(res.Wasm.SHA256), Size: res.Wasm.Size, Sig: key.SignSHA256Hex(res.Wasm.SHA256)}
	}
	if res.UI != nil {
		v.UI = &store.Artifact{URL: res.UI.URL, SHA256: strings.ToLower(res.UI.SHA256), Size: res.UI.Size, Sig: key.SignSHA256Hex(res.UI.SHA256)}
	}
	primary, ok := Primary(checks)
	if res.Kind == domain.KindStorage {
		// Every build of a storage plugin is signed, over its name, version,
		// platform and sha256: a filex that lists the artifact key in
		// FILEX_PLUGIN_TRUSTED_KEYS installs only builds the store vouched
		// for, as the plugin, version and platform it vouched for them.
		if len(res.Binaries) == 0 {
			return store.Version{}, refuse(Conflict, "resolution_unreadable", "The storage plugin's submission names no build.")
		}
		v.Binaries = signBuilds(res.Name, res.Version, res.Binaries, key)
		if !ok || primary.Storage == nil || primary.Storage.Summary == nil {
			return store.Version{}, refuse(Conflict, "checks_not_passed", "No plugin validator's conformance run passed.")
		}
		c := *primary.Storage.Summary
		c.Capabilities = append([]string{}, c.Capabilities...)
		v.Conformance = &c
	}
	rows := primary.Permissions
	if !ok || len(rows) == 0 {
		rows = nil
		for _, p := range res.Permissions {
			rows = append(rows, domain.PermissionRow{ID: p, Label: p})
		}
	}
	if rows == nil {
		rows = []domain.PermissionRow{}
	}
	v.PermissionRows, _ = json.Marshal(rows)
	type lang struct {
		Code    string `json:"code"`
		Percent int    `json:"percent"`
	}
	langs := []lang{}
	for _, l := range primary.Languages {
		langs = append(langs, lang{l.Code, l.Percent})
	}
	if len(langs) == 0 && len(primary.Languages) == 0 {
		for _, code := range res.Languages {
			langs = append(langs, lang{code, 0})
		}
	}
	v.Languages, _ = json.Marshal(langs)
	if len(primary.Raw) > 0 {
		var raw struct {
			FileTypes json.RawMessage `json:"file_types"`
		}
		if json.Unmarshal(primary.Raw, &raw) == nil && len(raw.FileTypes) > 0 && string(raw.FileTypes) != "null" {
			v.FileTypes = raw.FileTypes
		}
	}
	var err error
	v.ID, err = store.InsertVersion(ctx, tx, v)
	if errors.Is(err, store.ErrConflict) {
		return store.Version{}, refuse(Conflict, "version_exists", "%s %s is already listed.", app.Name, res.Version)
	}
	if err != nil {
		return store.Version{}, err
	}
	// The kinds of file it works with, for the catalog's file-type search,
	// in the approval's own transaction (derived from the manifest; a
	// manifest the extractor cannot read is left to the start-up backfill).
	if set, err := filetypes.Extract(res.Manifest); err != nil {
		s.log().Warn("file types: manifest not read", "app", app.Name, "version", v.Version, "err", err)
	} else if err := store.PutFileTypes(ctx, tx, v.ID, set); err != nil {
		return store.Version{}, err
	}
	// The conversions its listing declares (checked with the consent file:
	// the schema, and every engine they need is one the manifest asks for).
	if len(res.Consent.Conversions) > 0 {
		if err := store.PutConversions(ctx, tx, v.ID, res.Consent.Conversions, store.ConversionsFromListing, origin); err != nil {
			return store.Version{}, err
		}
	}
	return store.GetVersion(ctx, tx, app.ID, v.Version)
}

// conversionsOrigin says where an approved version's conversions were read:
// the commit's .filex-apps.json, or the operator's listing file (its name
// and sha256, as the import kept it) standing in for it.
func conversionsOrigin(res *domain.Resolution, operatorListing json.RawMessage) string {
	at := res.RepoOwner + "/" + res.RepoName + "@" + strings.ToLower(res.CommitSHA)
	if operatorListing == nil {
		return at + ":" + consent.FileName
	}
	var o struct {
		Source string `json:"source"`
	}
	_ = json.Unmarshal(operatorListing, &o)
	return at + ", operator listing " + o.Source
}

// canonicalRange is the range in filexrange's normal form (the resolver
// already writes it so; a range it cannot read is kept as it came).
func canonicalRange(s string) string {
	r, err := filexrange.Parse(s)
	if err != nil {
		return s
	}
	return r.Canonical()
}

func knownCategories(ctx context.Context, q db.Querier, ids []string) ([]string, error) {
	all, err := store.ListCategories(ctx, q)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, c := range all {
		known[c.ID] = true
	}
	out := []string{}
	for _, id := range ids {
		if known[id] && len(out) < 3 {
			out = append(out, id)
		}
	}
	return out, nil
}

// listingMedia picks the icon and the screenshots among the mirrored media
// (only images that are really stored).
func listingMedia(ctx context.Context, q db.Querier, refs []domain.MediaRef) (string, []store.AppMedia, error) {
	var shas []string
	for _, m := range refs {
		shas = append(shas, m.SHA256)
	}
	types, err := store.MediaTypes(ctx, q, shas)
	if err != nil {
		return "", nil, err
	}
	icon := ""
	shots := []store.AppMedia{}
	for _, m := range refs {
		t, ok := types[m.SHA256]
		if !ok {
			continue
		}
		switch m.Role {
		case "icon":
			if icon == "" {
				icon = m.SHA256
			}
		case "screenshot":
			if len(shots) < 8 {
				shots = append(shots, store.AppMedia{SHA256: m.SHA256, ContentType: t, Position: len(shots), Caption: map[string]string(m.Caption)})
			}
		}
	}
	return icon, shots, nil
}

// ManifestLabel is the manifest's `label` ({lang: text} or a string), else
// {"en": name}.
func ManifestLabel(manifest json.RawMessage, name string) map[string]string {
	var m struct {
		Label json.RawMessage `json:"label"`
	}
	if json.Unmarshal(manifest, &m) == nil && len(m.Label) > 0 {
		var t map[string]string
		if json.Unmarshal(m.Label, &t) == nil && len(t) > 0 {
			if t["en"] == "" {
				t["en"] = name
			}
			return t
		}
		var s string
		if json.Unmarshal(m.Label, &s) == nil && s != "" {
			return map[string]string{"en": s}
		}
	}
	return map[string]string{"en": name}
}

// ManifestEngines lists the engines a manifest asks for ({"ffmpeg": true}).
func ManifestEngines(manifest json.RawMessage) []string {
	var m struct {
		Engines map[string]bool `json:"engines"`
	}
	out := []string{}
	if json.Unmarshal(manifest, &m) != nil {
		return out
	}
	for k, on := range m.Engines {
		if on {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Reject ends a submission waiting for review, with a reason the publisher
// reads.
func (s *Service) Reject(ctx context.Context, id int64, d Decision) (store.Submission, error) {
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		sub, err := s.lockVisible(ctx, tx, id, d.Actor, true)
		if err != nil {
			return err
		}
		switch domain.SubmissionStatus(sub.Status) {
		case domain.SubNeedsReview:
		case domain.SubApproved, domain.SubRejected, domain.SubWithdrawn:
			return refuse(Conflict, "already_decided", "The submission is %s.", sub.Status)
		default:
			return refuse(Conflict, "not_pending", "The submission is %s, not waiting for review.", sub.Status)
		}
		if _, err := store.InsertReview(ctx, tx, store.Review{SubmissionID: id, ReviewerID: d.Actor.AccountID, Decision: "reject", Note: d.Note}); err != nil {
			return err
		}
		if err := store.Decide(ctx, tx, id, string(domain.SubRejected), d.Actor.AccountID, nil); err != nil {
			return err
		}
		if _, err = audit.Append(ctx, tx, s.entry(d.Actor, "submission.reject", "submission", id, map[string]any{"reason": d.Note})); err != nil {
			return err
		}
		op, err := store.OperatorListing(ctx, tx, id)
		if err != nil || op != nil {
			return err
		}
		return notifyDecision(ctx, tx, notify.KindRejected, sub, d)
	})
	if err != nil {
		return store.Submission{}, err
	}
	return store.GetSubmission(ctx, s.Pool, id)
}

// notifyDecision tells the publisher's members, but the one who decided,
// that their submission was approved or not (the reviewer's note is the
// reason of a rejection).
func notifyDecision(ctx context.Context, tx pgx.Tx, kind string, sub store.Submission, d Decision) error {
	members, err := store.PublisherMemberIDs(ctx, tx, sub.PublisherID)
	if err != nil {
		return err
	}
	reason := ""
	if kind == notify.KindRejected {
		reason = d.Note
	}
	_, err = notify.Emit(ctx, tx, notify.DecisionEvent(kind, sub, members, d.Actor.AccountID, reason))
	return err
}

// Comment leaves a note for the publisher without deciding.
func (s *Service) Comment(ctx context.Context, id int64, d Decision) (store.Review, error) {
	var out store.Review
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := s.lockVisible(ctx, tx, id, d.Actor, true); err != nil {
			return err
		}
		var err error
		out, err = store.InsertReview(ctx, tx, store.Review{SubmissionID: id, ReviewerID: d.Actor.AccountID, Decision: "comment", Note: d.Note})
		if err != nil {
			return err
		}
		_, err = audit.Append(ctx, tx, s.entry(d.Actor, "submission.comment", "submission", id, map[string]any{"review": out.ID}))
		return err
	})
	return out, err
}

// Recheck runs the dry runs of a submission waiting for review again (for
// example after a validator moved to a new filex).
func (s *Service) Recheck(ctx context.Context, id int64, d Decision) (store.Submission, error) {
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		sub, err := s.lockVisible(ctx, tx, id, d.Actor, true)
		if err != nil {
			return err
		}
		if sub.Status != string(domain.SubNeedsReview) {
			return refuse(Conflict, "not_pending", "Only a submission waiting for review is rechecked; this one is %s.", sub.Status)
		}
		if _, err := store.Transition(ctx, tx, id, sub.Attempt, strs(domain.SubNeedsReview), string(domain.SubValidating)); err != nil {
			return err
		}
		if err := store.DeleteChecks(ctx, tx, id, sub.Attempt, domain.CheckDryRun); err != nil {
			return err
		}
		if _, err := store.Enqueue(ctx, tx, store.JobDryRun, SubmissionJob{SubmissionID: id, Attempt: sub.Attempt}, time.Time{}); err != nil {
			return err
		}
		_, err = audit.Append(ctx, tx, s.entry(d.Actor, "submission.recheck", "submission", id, map[string]any{"attempt": sub.Attempt}))
		return err
	})
	if err != nil {
		return store.Submission{}, err
	}
	s.kick()
	return store.GetSubmission(ctx, s.Pool, id)
}

func (s *Service) entry(a Actor, action, targetType string, targetID any, data map[string]any) audit.Entry {
	id := a.AccountID
	var actor *int64
	if id != 0 {
		actor = &id
	}
	role := a.Role
	if role == "" {
		role = audit.RoleSystem
	}
	return audit.Entry{ActorAccountID: actor, ActorRole: role, Action: action, TargetType: targetType,
		TargetID: fmt.Sprint(targetID), IP: a.IP, Data: data}
}

// Diff compares a resolution with the newest approved version of the same
// app (for the reviewer).
type Diff struct {
	From               string   `json:"from"`
	PermissionsAdded   []string `json:"permissions_added"`
	PermissionsRemoved []string `json:"permissions_removed"`
	FilexFrom          string   `json:"filex_from"`
	FilexTo            string   `json:"filex_to"`
	ModuleChanged      bool     `json:"module_changed"`
	UIChanged          bool     `json:"ui_changed"`
}

// MakeDiff compares res against prev.
func MakeDiff(prev store.Version, res *domain.Resolution) Diff {
	d := Diff{From: prev.Version, FilexFrom: prev.FilexRange, FilexTo: res.FilexRange,
		PermissionsAdded: []string{}, PermissionsRemoved: []string{}}
	had := map[string]bool{}
	for _, p := range prev.Permissions {
		had[p] = true
	}
	now := map[string]bool{}
	for _, p := range res.Permissions {
		now[p] = true
		if !had[p] {
			d.PermissionsAdded = append(d.PermissionsAdded, p)
		}
	}
	for _, p := range prev.Permissions {
		if !now[p] {
			d.PermissionsRemoved = append(d.PermissionsRemoved, p)
		}
	}
	sort.Strings(d.PermissionsAdded)
	sort.Strings(d.PermissionsRemoved)
	d.ModuleChanged = shaOf(prev.Wasm) != artifactSHA(res.Wasm)
	d.UIChanged = shaOf(prev.UI) != artifactSHA(res.UI)
	return d
}

func shaOf(a *store.Artifact) string {
	if a == nil {
		return ""
	}
	return strings.ToLower(a.SHA256)
}

func artifactSHA(a *domain.Artifact) string {
	if a == nil {
		return ""
	}
	return strings.ToLower(a.SHA256)
}

// FormatID is a submission id as text (audit target ids).
func FormatID(id int64) string { return strconv.FormatInt(id, 10) }
