package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/safehttp"
)

// Outcome statuses of one attempt.
const (
	StatusDelivered = "delivered"
	StatusFailed    = "failed"
)

// Result is one attempt to deliver.
type Result struct {
	Status     string // delivered | failed
	HTTPStatus int    // the receiver's answer (0: none)
	Error      string // why it failed, without the address
	// Retry: worth another attempt (no answer, a timeout, 408, 425, 429,
	// 5xx). Another 4xx is final.
	Retry bool
	// Gone: the push service says the subscription no longer exists (404,
	// 410); the store forgets it.
	Gone bool
}

// SendTimeout bounds one attempt, body included.
const SendTimeout = 10 * time.Second

// Sender talks to webhooks and push services through the guarded client:
// HTTPS only (plain http only with APP_ENV=development), any host, but never
// a private, loopback, link-local or metadata address - checked after DNS,
// on every connection and every redirect (internal/safehttp).
type Sender struct {
	Client *safehttp.Client
	// UserAgent names the store (filex-apps-notify/<version> (+https://apps.example.com)).
	UserAgent string
	// Now is the clock (tests).
	Now func() time.Time
}

func (s *Sender) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Webhook is an opened webhook: its address and secrets in the clear.
type Webhook struct {
	ID          string
	Name        string
	Format      string
	URL         string
	Secret      []byte // generic
	HeaderName  string // generic, optional
	HeaderValue string
}

// SendWebhook posts one message to a webhook in its format.
func (s *Sender) SendWebhook(ctx context.Context, w Webhook, m Message, meta Meta) Result {
	body, err := Body(w.Format, m, meta)
	if err != nil {
		return Result{Status: StatusFailed, Error: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return Result{Status: StatusFailed, Error: "the address is not valid"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", s.UserAgent)
	if w.Format == FormatGeneric {
		ts := s.now().Unix()
		req.Header.Set(HeaderEvent, meta.Event)
		req.Header.Set(HeaderDelivery, meta.Delivery)
		req.Header.Set(HeaderTimestamp, strconv.FormatInt(ts, 10))
		if len(w.Secret) > 0 {
			req.Header.Set(HeaderSignature, Sign(w.Secret, ts, body))
		}
		if w.HeaderName != "" {
			req.Header.Set(w.HeaderName, w.HeaderValue)
		}
	}
	return s.do(req, false)
}

// SendPush sends one message to a browser's push service.
func (s *Sender) SendPush(ctx context.Context, v *VAPID, t PushTarget, m Message, tag string) Result {
	if v == nil {
		return Result{Status: StatusFailed, Error: "browser notifications are not set up on this store (no VAPID key)"}
	}
	body, err := EncryptPush(pushPayload(m, tag), t, nil, nil)
	if err != nil {
		return Result{Status: StatusFailed, Error: "the subscription's keys are not usable: " + err.Error()}
	}
	auth, err := v.Authorization(t.Endpoint, s.now())
	if err != nil {
		return Result{Status: StatusFailed, Error: "the subscription's endpoint is not valid"}
	}
	ctx, cancel := context.WithTimeout(ctx, SendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{Status: StatusFailed, Error: "the subscription's endpoint is not valid"}
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", "86400")
	req.Header.Set("Urgency", "normal")
	req.Header.Set("Authorization", auth)
	req.Header.Set("User-Agent", s.UserAgent)
	return s.do(req, true)
}

// do sends a request through the guard and judges the answer.
func (s *Sender) do(req *http.Request, push bool) Result {
	if s.Client == nil {
		return Result{Status: StatusFailed, Error: "no outbound client"}
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return Result{Status: StatusFailed, Error: describeSendError(err), Retry: !safehttp.Refused(err)}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	code := resp.StatusCode
	if code/100 == 2 {
		return Result{Status: StatusDelivered, HTTPStatus: code}
	}
	r := Result{Status: StatusFailed, HTTPStatus: code, Error: "the receiver answered HTTP " + strconv.Itoa(code)}
	switch {
	case code == http.StatusRequestTimeout || code == http.StatusTooEarly || code == http.StatusTooManyRequests || code >= 500:
		r.Retry = true
	case push && (code == http.StatusNotFound || code == http.StatusGone):
		r.Gone = true
		r.Error = "the push service says this browser's subscription is gone (HTTP " + strconv.Itoa(code) + ")"
	case code/100 == 3:
		r.Error = "the receiver answered with a redirect the store does not follow (HTTP " + strconv.Itoa(code) + ")"
	}
	return r
}

// describeSendError says why an attempt got no answer, in words that never
// carry the address (a Slack or Discord address is a credential).
func describeSendError(err error) string {
	switch {
	case errors.Is(err, safehttp.ErrNotHTTPS):
		return "only https addresses are allowed"
	case errors.Is(err, safehttp.ErrPrivateAddress):
		return "the address resolves to a private, loopback or reserved network address, which the store does not call"
	case errors.Is(err, safehttp.ErrTooManyRedirects):
		return "too many redirects"
	case errors.Is(err, safehttp.ErrBadURL), errors.Is(err, safehttp.ErrHostNotAllowed):
		return "the address is not valid"
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("no answer within %s", SendTimeout)
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "the host name does not resolve"
	}
	var ue *url.Error
	if errors.As(err, &ue) && ue.Timeout() {
		return fmt.Sprintf("no answer within %s", SendTimeout)
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		return "could not connect: " + oe.Op + " failed"
	}
	msg := err.Error()
	if strings.Contains(msg, "certificate") || strings.Contains(msg, "x509") || strings.Contains(msg, "tls") {
		return "the receiver's TLS certificate is not trusted"
	}
	return "the request failed"
}
