package pipeline

import (
	"github.com/brf-tech/filex-apps/backend/internal/domain"
)

// transitions is the submission state machine (docs/ARCHITECTURE.md
// "Submission pipeline"):
//
//	queued --> resolving --> validating --> needs_review --> approved
//	   |           |              |              |      \--> rejected
//	   |           v              v              |
//	   |         failed  <--------+              +--> (recheck: back to validating)
//	   +--> withdrawn  (any time before a decision)
//
// resolving goes back to queued when the resolver hit a network error and
// the job is retried; failed goes back to queued when the publisher retries
// (a new attempt).
var transitions = map[domain.SubmissionStatus][]domain.SubmissionStatus{
	domain.SubQueued:      {domain.SubResolving, domain.SubFailed, domain.SubWithdrawn},
	domain.SubResolving:   {domain.SubValidating, domain.SubFailed, domain.SubQueued, domain.SubWithdrawn},
	domain.SubValidating:  {domain.SubNeedsReview, domain.SubFailed, domain.SubWithdrawn},
	domain.SubNeedsReview: {domain.SubApproved, domain.SubRejected, domain.SubValidating, domain.SubWithdrawn},
	domain.SubFailed:      {domain.SubQueued},
}

// CanTransition reports whether the state machine allows from -> to.
func CanTransition(from, to domain.SubmissionStatus) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// IsOpen reports a submission still in the pipeline (it may be withdrawn,
// and blocks another submission of the same repository and tag).
func IsOpen(s domain.SubmissionStatus) bool {
	switch s {
	case domain.SubQueued, domain.SubResolving, domain.SubValidating, domain.SubNeedsReview:
		return true
	}
	return false
}

// IsFinal reports a decided or abandoned submission (nothing leaves it).
func IsFinal(s domain.SubmissionStatus) bool {
	return len(transitions[s]) == 0
}

func strs(ss ...domain.SubmissionStatus) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = string(s)
	}
	return out
}
