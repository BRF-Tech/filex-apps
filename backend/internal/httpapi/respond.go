package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/brf-tech/filex-apps/backend/internal/pipeline"
)

// Error is the one error body every endpoint answers with
// (api/openapi.yaml #/components/schemas/Error).
type Error struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, Error{Error: code, Message: msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "no-store")
	}
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// maxBody bounds a JSON request body.
const maxBody = 1 << 20

// badRequest is a decoding or validation failure (400).
type badRequest struct {
	code string
	msg  string
}

func (e *badRequest) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return &badRequest{code: "invalid_field", msg: fmt.Sprintf(format, args...)}
}

// decode reads one JSON object, refusing unknown fields (the contract's
// additionalProperties: false), trailing data and text with NUL bytes or
// invalid UTF-8 (PostgreSQL refuses both).
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		return &badRequest{code: "bad_request", msg: "The body must be application/json."}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		return &badRequest{code: "bad_request", msg: "The body is too large or unreadable."}
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	if !utf8.Valid(body) || bytes.Contains(body, []byte(`\u0000`)) || bytes.IndexByte(body, 0) >= 0 {
		return &badRequest{code: "bad_request", msg: "The body holds invalid text."}
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &badRequest{code: "bad_request", msg: "The body is not the expected JSON: " + err.Error()}
	}
	if dec.More() {
		return &badRequest{code: "bad_request", msg: "The body holds more than one JSON value."}
	}
	return nil
}

// fail answers an error: a refusal with its code, a bad request, else 500
// (logged, nothing internal leaked).
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ref *pipeline.Refusal
	var bad *badRequest
	switch {
	case errors.As(err, &bad):
		writeError(w, http.StatusBadRequest, bad.code, bad.msg)
	case errors.As(err, &ref):
		status := map[pipeline.RefusalKind]int{
			pipeline.Invalid: http.StatusBadRequest, pipeline.Forbidden: http.StatusForbidden,
			pipeline.NotFound: http.StatusNotFound, pipeline.Conflict: http.StatusConflict,
			pipeline.Unavailable: http.StatusServiceUnavailable, pipeline.Gone: http.StatusGone,
			pipeline.Unauthorized: http.StatusUnauthorized,
		}[ref.Kind]
		if status == 0 {
			status = http.StatusBadRequest
		}
		if ref.Submission != nil {
			writeJSON(w, status, map[string]any{"error": ref.Code, "message": ref.Message, "submission": h.submissionView(*ref.Submission)})
			return
		}
		writeError(w, status, ref.Code, ref.Message)
	default:
		h.log.Error("request failed", "method", r.Method, "path", logPath(r.URL.Path), "rid", requestID(r), "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "Something went wrong on the server.")
	}
}

// page reads page and per_page (1-based; per_page 1..100, default 50).
func page(r *http.Request) (int, int, error) {
	p, pp := 1, 50
	if v := r.URL.Query().Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100000 {
			return 0, 0, invalid("page must be a positive integer.")
		}
		p = n
	}
	if v := r.URL.Query().Get("per_page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return 0, 0, invalid("per_page must be between 1 and 100.")
		}
		pp = n
	}
	return p, pp, nil
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func rfcPtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return rfc(*t)
}

// cleanText trims a person's text and refuses control characters other
// than newlines and tabs.
func cleanText(field, s string, min, max int) (string, error) {
	s = strings.TrimSpace(s)
	if n := utf8.RuneCountInString(s); n < min || n > max {
		if min > 0 && n == 0 {
			return "", invalid("%s is required.", field)
		}
		return "", invalid("%s must be %d to %d characters.", field, min, max)
	}
	for _, r := range s {
		if r < ' ' && r != '\n' && r != '\t' && r != '\r' || r == 0x7f {
			return "", invalid("%s holds a control character.", field)
		}
	}
	return s, nil
}
