// Package notify tells people what happened in the store (docs/ARCHITECTURE.md
// "Notifications"): an event is raised in the same transaction as the change
// it reports, a job fans it out to the channels that chose it - a person's
// webhooks (generic JSON signed with HMAC-SHA256, Slack, Discord, Teams) and
// browsers (Web Push with VAPID) - and one job per channel sends it, with a
// time limit and a few retries. Every word a notification says is written
// here, on the server.
//
// Who hears what:
//
//   - administrators (write or above on the administration repository, read
//     again from GitHub when the event goes out, failing closed): a new app or
//     a new version waiting for review, a publisher waiting to be activated,
//     a submission the store could not check (validators or GitHub away);
//   - a publisher's members: their submission approved or not approved;
//   - everyone who chose it: the store knows a newer filex release.
package notify

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// Event kinds. The strings are part of the generic webhook's body
// (`event`) and of the API; never rename one.
const (
	// KindAppSubmitted: a new app's submission passed its checks and waits
	// for review (administrators).
	KindAppSubmitted = "submission.app_new"
	// KindVersionSubmitted: a new version of a listed app waits for review
	// (administrators).
	KindVersionSubmitted = "submission.version_new"
	// KindPublisherRequested: someone asked for a publisher, which waits for
	// an administrator to activate it (administrators).
	KindPublisherRequested = "publisher.requested"
	// KindPipelineError: a submission failed because the store could not
	// run its checks - the validators or GitHub did not answer - not because
	// of the submission (administrators).
	KindPipelineError = "pipeline.error"
	// KindApproved: a submission was approved (the publisher's members).
	KindApproved = "submission.approved"
	// KindRejected: a submission was not approved (the publisher's members).
	KindRejected = "submission.rejected"
	// KindFilexReleased: the store knows a newer filex release than before
	// (everyone who chose it).
	KindFilexReleased = "filex.released"
	// KindTest is a channel's test, sent on request and never stored.
	KindTest = "test"
)

// Audiences of notification_events.
const (
	AudienceAdmin    = "admin"
	AudienceAccounts = "accounts"
	AudienceAll      = "all"
)

// KindInfo is one event a person may choose.
type KindInfo struct {
	Kind string `json:"kind"`
	// Admin: only administrators may choose it.
	Admin bool `json:"admin"`
}

// Kinds are the events a channel may choose, in the order the page lists
// them.
var Kinds = []KindInfo{
	{KindApproved, false},
	{KindRejected, false},
	{KindFilexReleased, false},
	{KindAppSubmitted, true},
	{KindVersionSubmitted, true},
	{KindPublisherRequested, true},
	{KindPipelineError, true},
}

// Known reports an event a channel may choose.
func Known(kind string) bool {
	for _, k := range Kinds {
		if k.Kind == kind {
			return true
		}
	}
	return false
}

// AdminOnly reports an administrators' event.
func AdminOnly(kind string) bool {
	for _, k := range Kinds {
		if k.Kind == kind {
			return k.Admin
		}
	}
	return false
}

// KindsFor are the events an account may choose.
func KindsFor(admin bool) []KindInfo {
	out := []KindInfo{}
	for _, k := range Kinds {
		if admin || !k.Admin {
			out = append(out, k)
		}
	}
	return out
}

// Event is what a change raises.
type Event struct {
	Kind     string
	Audience string
	// Accounts are the audience "accounts".
	Accounts []int64
	// DedupeKey makes the event happen once ("" for none).
	DedupeKey string
	Data      map[string]any
}

// Emit stores the event and queues its fan-out, in the caller's
// transaction, so the event exists exactly when the change does. An event
// whose dedupe key was used before is dropped (false).
func Emit(ctx context.Context, q store.Q, ev Event) (bool, error) {
	if ev.Audience == AudienceAccounts && len(ev.Accounts) == 0 {
		return false, nil
	}
	data, err := json.Marshal(ev.Data)
	if err != nil {
		return false, err
	}
	id, inserted, err := store.InsertNotifyEvent(ctx, q, store.NotifyEvent{Kind: ev.Kind, Audience: ev.Audience,
		AccountIDs: ev.Accounts, DedupeKey: ev.DedupeKey, Data: data})
	if err != nil || !inserted {
		return false, err
	}
	if _, err := store.Enqueue(ctx, q, store.JobNotifyEvent, EventJob{EventID: id}, time.Time{}); err != nil {
		return false, err
	}
	return true, nil
}

// EventJob is a notify_event job's payload.
type EventJob struct {
	EventID int64 `json:"event_id"`
}

// DeliverJob is a notify_deliver job's payload.
type DeliverJob struct {
	DeliveryID int64 `json:"delivery_id"`
}

// submissionData is what every submission event carries.
func submissionData(sub store.Submission) map[string]any {
	d := map[string]any{
		"submission_id": sub.ID,
		"app":           sub.Name,
		"version":       sub.Version,
		"publisher":     sub.PublisherID,
		"repo":          sub.Repo,
		"ref":           sub.Ref,
		"kind":          sub.Kind,
	}
	if sub.Publisher.Name != "" {
		d["publisher_name"] = sub.Publisher.Name
	}
	if sub.SubmitterLogin != "" {
		d["submitter"] = sub.SubmitterLogin
	}
	return d
}

// ReviewEvent: a submission reached review. newApp tells a first version
// from an update. Once per submission and attempt: a reviewer's re-run of
// the dry runs does not raise it again.
func ReviewEvent(sub store.Submission, newApp bool) Event {
	kind := KindVersionSubmitted
	if newApp {
		kind = KindAppSubmitted
	}
	return Event{Kind: kind, Audience: AudienceAdmin, Data: submissionData(sub),
		DedupeKey: "review:" + strconv.FormatInt(sub.ID, 10) + ":" + strconv.Itoa(sub.Attempt)}
}

// DecisionEvent: a submission was approved (KindApproved) or not
// (KindRejected, with the reviewer's reason), for the publisher's members
// but the one who decided.
func DecisionEvent(kind string, sub store.Submission, members []int64, decider int64, reason string) Event {
	var to []int64
	for _, m := range members {
		if m != decider {
			to = append(to, m)
		}
	}
	d := submissionData(sub)
	if reason != "" {
		d["reason"] = reason
	}
	return Event{Kind: kind, Audience: AudienceAccounts, Accounts: to, Data: d,
		DedupeKey: "decision:" + strconv.FormatInt(sub.ID, 10)}
}

// PipelineErrorEvent: the store could not check a submission.
func PipelineErrorEvent(sub store.Submission, code, message string) Event {
	d := submissionData(sub)
	d["code"], d["message"] = code, message
	return Event{Kind: KindPipelineError, Audience: AudienceAdmin, Data: d,
		DedupeKey: "pipeline-error:" + strconv.FormatInt(sub.ID, 10) + ":" + strconv.Itoa(sub.Attempt)}
}

// PublisherRequestEvent: a publisher was asked for.
func PublisherRequestEvent(pub store.Publisher, requester string) Event {
	d := map[string]any{"publisher": pub.ID, "publisher_name": pub.Name, "github_owner": pub.GitHubOwner,
		"owner_type": pub.OwnerType}
	if requester != "" {
		d["requested_by"] = requester
	}
	return Event{Kind: KindPublisherRequested, Audience: AudienceAdmin, Data: d, DedupeKey: "publisher-request:" + pub.ID}
}

// FilexReleasedEvent: the store knows filex version (previous: what it knew
// before). Once per version.
func FilexReleasedEvent(version, previous string) Event {
	return Event{Kind: KindFilexReleased, Audience: AudienceAll, DedupeKey: "filex:" + version,
		Data: map[string]any{"version": version, "previous": previous}}
}

// PipelineErrorCodes are the failure codes that mean the store, not the
// submission, could not go on.
var PipelineErrorCodes = map[string]bool{
	"dry_run_error":         true, // the validators did not answer, attempts used
	"validator_unavailable": true, // no validator configured for the kind
	"resolve_error":         true, // GitHub or the network did not answer, attempts used
	"resolver_unavailable":  true, // the resolver is not configured
}
