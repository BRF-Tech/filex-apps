package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/brf-tech/filex-apps/backend/internal/audit"
	"github.com/brf-tech/filex-apps/backend/internal/consent"
	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/filetypes"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// ConversionsRequest is an operator's word on what an approved version of
// an official publisher's app converts (`fapps-server conversions`): the
// `conversions` of the listing file the operator gives, held to the same
// schema as a repository's .filex-apps.json (internal/consent).
type ConversionsRequest struct {
	Actor   Actor
	App     string
	Version string
	Listing *domain.OperatorImport
}

// ConversionsResult is what SetConversions changed.
type ConversionsResult struct {
	App     string
	Version string
	// Entries and Pairs count what the version declares now (0: nothing).
	Entries, Pairs int
	// Before and After are the sha256 of the declaration before and after
	// (empty: none); equal when nothing changed.
	Before, After string
	// Source is where the declaration it replaced came from ("" for none).
	Source string
}

// SetConversions replaces the conversions an approved version declares with
// the operator's listing's, or removes them when the listing declares none.
// Only an official, verified publisher's app, and only with a listing that
// names that publisher: the operator stands in for the publisher here as an
// operator import does. Every engine a conversion needs must be one the
// version's manifest asks for. The change is audited
// (version.conversions); the signed index does not carry conversions, so no
// index is published.
func (s *Service) SetConversions(ctx context.Context, in ConversionsRequest) (ConversionsResult, error) {
	out := ConversionsResult{App: in.App, Version: in.Version}
	if in.Listing == nil {
		return out, refuse(Invalid, "invalid_field", "A listing is needed.")
	}
	convs := in.Listing.Listing.Conversions
	err := db.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		app, err := lockApp(ctx, tx, in.App)
		if err != nil {
			return err
		}
		pub, err := store.GetPublisher(ctx, tx, app.PublisherID)
		if err != nil {
			return err
		}
		if !pub.Official || !pub.Verified {
			return refuse(Conflict, "publisher_not_official", "%s is listed under %q, which is not official and verified: its conversions come with its own .filex-apps.json.", app.Name, pub.ID)
		}
		if in.Listing.Listing.Publisher != pub.ID {
			return refuse(Conflict, "consent_publisher", "The listing names the publisher %q; %s is listed under %q.", in.Listing.Listing.Publisher, app.Name, pub.ID)
		}
		v, err := store.GetVersion(ctx, tx, app.ID, in.Version)
		if errors.Is(err, store.ErrNotFound) {
			return refuse(NotFound, "not_found", "%s has no version %s.", app.Name, in.Version)
		}
		if err != nil {
			return err
		}
		if problems := consent.CheckNeeds(convs, v.Permissions, ManifestEngines(v.Manifest)); len(problems) > 0 {
			return refuse(Invalid, "consent_invalid", "%s %s: %s", app.Name, v.Version, strings.Join(problems, "; "))
		}
		prev, err := store.ConversionsOf(ctx, tx, v.ID)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			return err
		default:
			out.Before, out.Source = declarationSum(prev.Conversions), prev.Source
		}
		out.After = declarationSum(convs)
		out.Entries, out.Pairs = len(convs), consent.Pairs(convs)
		if out.Before == out.After {
			return nil
		}
		if err := store.PutConversions(ctx, tx, v.ID, convs, store.ConversionsFromOperator, in.Listing.Source); err != nil {
			return err
		}
		_, err = audit.Append(ctx, tx, s.entry(in.Actor, "version.conversions", "version", app.Name+"@"+v.Version, map[string]any{
			"app": app.Name, "version": v.Version, "listing": in.Listing.Source,
			"entries": out.Entries, "pairs": out.Pairs,
			"from": map[string]any{"sha256": out.Before, "source": out.Source}, "to": map[string]any{"sha256": out.After},
		}))
		return err
	})
	return out, err
}

// declarationSum is the sha256 of a declaration's JSON ("" for none), for
// the audit row and to tell a change from a repeat.
func declarationSum(convs []filetypes.Conversion) string {
	if len(convs) == 0 {
		return ""
	}
	b, _ := json.Marshal(convs)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
