package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/brf-tech/filex-apps/backend/internal/audit"
	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// OperatorConsent is what an operator import puts in place of the
// repository's .filex-apps.json (docs/ARCHITECTURE.md "Operator import
// (official publisher)"): the consent check's sentence and the audit log's
// words.
const OperatorConsent = "operator import (official publisher, consent by ownership)"

// OperatorActor is the operator command's actor: the operator account (made
// on first use), acting as an administrator. It never signs in.
func (s *Service) OperatorActor(ctx context.Context) (Actor, error) {
	id, err := store.OperatorAccount(ctx, s.Pool)
	if err != nil {
		return Actor{}, err
	}
	return Actor{AccountID: id, Role: audit.RoleAdmin, Admin: true}, nil
}

// OfficialPublisher is the operator's word on an official publisher.
type OfficialPublisher struct {
	Actor    Actor
	ID       string
	Name     string
	Homepage string
	// Owner is the GitHub account or organization as GitHub answered it
	// (its numeric id is what the publisher is bound to).
	Owner Owner
}

// EnsureOfficialPublisher creates the publisher bound to the owner's numeric
// id when it does not exist (the operator account is its owner member), and
// makes it active, verified and official. An existing publisher bound to
// another GitHub owner is refused (owner_mismatch). Every change is audited;
// nothing changes when the publisher already stands as asked.
func (s *Service) EnsureOfficialPublisher(ctx context.Context, in OfficialPublisher) (store.Publisher, bool, error) {
	if in.Owner.ID == 0 || in.Owner.Login == "" || (in.Owner.Type != "User" && in.Owner.Type != "Organization") {
		return store.Publisher{}, false, refuse(Invalid, "invalid_field", "The GitHub owner needs a numeric id, a login and a type.")
	}
	created := false
	pub, err := store.GetPublisher(ctx, s.Pool, in.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
			var err error
			pub, err = store.CreatePublisher(ctx, tx, store.Publisher{
				ID: in.ID, Name: in.Name, GitHubOwner: in.Owner.Login, GitHubOwnerID: in.Owner.ID,
				OwnerType: in.Owner.Type, Homepage: in.Homepage,
			}, in.Actor.AccountID)
			if errors.Is(err, store.ErrConflict) {
				return refuse(Conflict, "publisher_exists", "The publisher id or the GitHub owner %s is already taken.", in.Owner.Login)
			}
			if err != nil {
				return err
			}
			_, err = audit.Append(ctx, tx, s.entry(in.Actor, "publisher.request", "publisher", pub.ID, map[string]any{
				"github_owner": in.Owner.Login, "github_owner_id": in.Owner.ID, "owner_type": in.Owner.Type, "via": "operator"}))
			return err
		})
		if err != nil {
			return store.Publisher{}, false, err
		}
		created = true
	case err != nil:
		return store.Publisher{}, false, err
	case pub.GitHubOwnerID != in.Owner.ID:
		return store.Publisher{}, false, refuse(Conflict, "owner_mismatch",
			"The publisher %q is bound to the GitHub owner %s (id %d), not to %s (id %d).", pub.ID, pub.GitHubOwner, pub.GitHubOwnerID, in.Owner.Login, in.Owner.ID)
	}
	wasActive := pub.Status == "active"
	if !(wasActive && pub.Verified && pub.Official) {
		active, yes := "active", true
		reason := ""
		if pub, _, err = s.UpdatePublisher(ctx, pub.ID, store.PublisherPatch{Status: &active, StatusReason: &reason, Verified: &yes, Official: &yes}, in.Actor); err != nil {
			return store.Publisher{}, created, err
		}
	}
	// Becoming active read the profile already (UpdatePublisher); an
	// active publisher's profile is read again here.
	if wasActive {
		pub = s.refreshProfile(ctx, pub, in.Actor)
	}
	return pub, created, nil
}

// ImportRequest is an operator import of an official publisher's repository
// at a tag, with the listing the operator gives in place of the repository's
// .filex-apps.json.
type ImportRequest struct {
	Actor     Actor
	Publisher string
	Repo      string
	Ref       string
	Note      string
	Listing   *domain.OperatorImport
	// Kind is "app" (the default) or "storage": a storage plugin of the
	// official publisher, listed from its release's filex-storage.json.
	Kind string
}

// Import queues an operator import. It checks synchronously what a web
// submission checks with the submitter's token, with ownership in place of
// the submitter's role: the publisher is active, official and verified; the
// listing names it; the repository is public, not archived, and its owner
// (numeric id, as GitHub answers now) is the publisher's bound owner. Then
// the whole pipeline runs as for any submission (resolve, policy, artifacts,
// media, the dry runs on every validator, the pins); the resolve checks the
// consent by ownership again against the commit it reads.
func (s *Service) Import(ctx context.Context, in ImportRequest) (store.Submission, error) {
	owner, name, ok := ParseRepo(in.Repo)
	if !ok {
		return store.Submission{}, refuse(Invalid, "invalid_field", "repo must be owner/name or a https://github.com/owner/name address.")
	}
	if !ValidRef(in.Ref) {
		return store.Submission{}, refuse(Invalid, "invalid_field", "ref must be a tag name.")
	}
	if in.Listing == nil {
		return store.Submission{}, refuse(Invalid, "invalid_field", "An operator import needs a listing.")
	}
	if in.Kind == "" {
		in.Kind = string(domain.SubmitApp)
	}
	if !domain.ValidSubmissionKind(in.Kind) {
		return store.Submission{}, refuse(Invalid, "invalid_field", "kind must be app or storage.")
	}
	repo := owner + "/" + name
	pub, err := store.GetPublisher(ctx, s.Pool, in.Publisher)
	if errors.Is(err, store.ErrNotFound) {
		return store.Submission{}, refuse(NotFound, "not_found", "No publisher %q.", in.Publisher)
	}
	if err != nil {
		return store.Submission{}, err
	}
	if pub.Status != "active" {
		return store.Submission{}, refuse(Forbidden, "publisher_not_active", "The publisher %q is %s.", pub.ID, pub.Status)
	}
	if !pub.Official || !pub.Verified {
		return store.Submission{}, refuse(Forbidden, "publisher_not_official",
			"An operator import is for an official, verified publisher; %q is official=%v, verified=%v. Everyone else submits through the web interface with a .filex-apps.json.",
			pub.ID, pub.Official, pub.Verified)
	}
	if in.Listing.Listing.Publisher != pub.ID {
		return store.Submission{}, refuse(Invalid, "consent_publisher", "The listing names the publisher %q, not %q.", in.Listing.Listing.Publisher, pub.ID)
	}
	if app, version, err := store.VersionAtRepoRef(ctx, s.Pool, repo, in.Ref); err != nil {
		return store.Submission{}, err
	} else if version != "" {
		return store.Submission{}, refuse(Conflict, "version_exists", "%s at %s is already listed as %s %s.", repo, in.Ref, app, version)
	}
	if id, err := store.FindOpenSubmission(ctx, s.Pool, repo, in.Ref); err == nil {
		return store.Submission{}, s.openRefusal(ctx, id)
	}
	if s.Repos == nil {
		return store.Submission{}, refuse(Unavailable, "github_unavailable", "No GitHub reader is configured.")
	}
	ghRepo, check, err := s.Repos.RepoAccess(ctx, repo, "")
	switch {
	case err != nil:
		return store.Submission{}, refuse(Unavailable, "github_unavailable", "GitHub could not be asked: %v", err)
	case check.Status == domain.CheckFailed:
		return store.Submission{}, refuse(Forbidden, orCode(check.Code, "repo_access_failed"), "%s", check.Message)
	case ghRepo == nil:
		return store.Submission{}, refuse(Unavailable, "github_unavailable", "GitHub did not show %s.", repo)
	case ghRepo.Owner.ID != pub.GitHubOwnerID:
		return store.Submission{}, refuse(Forbidden, "owner_mismatch",
			"%s belongs to %s (id %d), not to the publisher's GitHub owner %s (id %d): consent by ownership does not hold.",
			ghRepo.FullName, ghRepo.Owner.Login, ghRepo.Owner.ID, pub.GitHubOwner, pub.GitHubOwnerID)
	}
	check.Kind = domain.CheckRepoAccess
	if check.StartedAt.IsZero() {
		check.StartedAt = time.Now()
	}
	data := map[string]any{}
	if len(check.Data) > 0 {
		_ = json.Unmarshal(check.Data, &data)
	}
	data["permission"] = "not asked: " + OperatorConsent
	check.Data, _ = json.Marshal(data)
	check.Message = OperatorConsent
	repoName := ghRepo.Name
	if repoName == "" {
		repoName = name
	}
	full := ghRepo.Owner.Login + "/" + repoName
	listing, err := json.Marshal(in.Listing)
	if err != nil {
		return store.Submission{}, err
	}

	var id int64
	err = db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		id, err = store.InsertSubmissionKind(ctx, tx, pub.ID, in.Actor.AccountID, full, in.Ref, in.Note, in.Kind)
		if err != nil {
			return err
		}
		if err := store.SetOperatorListing(ctx, tx, id, listing); err != nil {
			return err
		}
		if err := store.InsertChecks(ctx, tx, id, 1, []domain.CheckResult{check}); err != nil {
			return err
		}
		if _, err := store.Enqueue(ctx, tx, store.JobResolve, SubmissionJob{SubmissionID: id, Attempt: 1}, time.Time{}); err != nil {
			return err
		}
		_, err = audit.Append(ctx, tx, s.entry(in.Actor, "submission.create", "submission", id, map[string]any{
			"publisher": pub.ID, "repo": full, "repo_id": ghRepo.ID, "owner_id": ghRepo.Owner.ID, "ref": in.Ref,
			"operator_import": true, "consent": OperatorConsent, "listing": in.Listing.Source}))
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

// operatorImport is the listing an operator import keeps with its
// submission, with the publisher's standing as the database says now; nil
// for a submission made through the web interface.
func operatorImport(ctx context.Context, q db.Querier, sub store.Submission) (*domain.OperatorImport, error) {
	raw, err := store.OperatorListing(ctx, q, sub.ID)
	if err != nil || raw == nil {
		return nil, err
	}
	var op domain.OperatorImport
	if err := json.Unmarshal(raw, &op); err != nil {
		return nil, errOperatorListing
	}
	pub, err := store.GetPublisher(ctx, q, sub.PublisherID)
	if err != nil {
		return nil, err
	}
	op.PublisherOfficial = pub.Official && pub.Status == "active"
	op.PublisherVerified = pub.Verified
	op.PublisherOwnerID = pub.GitHubOwnerID
	return &op, nil
}

var errOperatorListing = errors.New("the operator's listing kept with the submission is unreadable")
