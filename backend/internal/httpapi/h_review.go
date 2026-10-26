package httpapi

import (
	"net/http"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/pipeline"
	"github.com/brf-tech/filex-apps/backend/internal/session"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// reviewQueue: submissions waiting for a decision, oldest first.
func (h *handler) reviewQueue(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	pg, pp, err := page(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeSubmissionPage(w, r, store.SubmissionFilter{Status: string(domain.SubNeedsReview), OldestFirst: true},
		store.Page{Page: pg, PerPage: pp})
}

// reason reads a ReasonRequest.
func (h *handler) reason(w http.ResponseWriter, r *http.Request) (string, bool) {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decode(w, r, &body); err != nil {
		h.fail(w, r, err)
		return "", false
	}
	reason, err := cleanText("reason", body.Reason, 1, 2000)
	if err != nil {
		h.fail(w, r, err)
		return "", false
	}
	return reason, true
}

func (h *handler) approve(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	id, ok := submissionID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "No such submission.")
		return
	}
	var body struct {
		Note     string `json:"note"`
		Security bool   `json:"security"`
	}
	if err := decode(w, r, &body); err != nil {
		h.fail(w, r, err)
		return
	}
	note, err := cleanText("note", body.Note, 0, 8000)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	res, err := h.Service.Approve(r.Context(), id, pipeline.Decision{Actor: actor(r, p), Note: note, Security: body.Security})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	vd, err := h.versionDetailOf(r.Context(), res.App, res.Version)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"submission":   h.submissionView(res.Submission),
		"version":      vd,
		"index_serial": res.Serial,
	})
}

func (h *handler) reject(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	id, ok := submissionID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "No such submission.")
		return
	}
	reason, ok := h.reason(w, r)
	if !ok {
		return
	}
	sub, err := h.Service.Reject(r.Context(), id, pipeline.Decision{Actor: actor(r, p), Note: reason})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h.submissionView(sub))
}

func (h *handler) comment(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	id, ok := submissionID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "No such submission.")
		return
	}
	reason, ok := h.reason(w, r)
	if !ok {
		return
	}
	rv, err := h.Service.Comment(r.Context(), id, pipeline.Decision{Actor: actor(r, p), Note: reason})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, reviewViewOf(rv))
}

func (h *handler) recheck(w http.ResponseWriter, r *http.Request, p *session.Principal) {
	id, ok := submissionID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "No such submission.")
		return
	}
	sub, err := h.Service.Recheck(r.Context(), id, pipeline.Decision{Actor: actor(r, p)})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, h.submissionView(sub))
}
