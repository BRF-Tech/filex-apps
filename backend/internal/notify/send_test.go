package notify

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brf-tech/filex-apps/backend/internal/safehttp"
)

// receiver is a webhook receiver on 127.0.0.1 over TLS; a sender reaches it
// only with an address rule that admits loopback (the tests' stand-in for a
// public address).
type receiver struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []*http.Request
	body [][]byte
	// answer is the status of each request in turn (the last one repeats).
	answer []int
}

func newReceiver(t *testing.T, answer ...int) *receiver {
	t.Helper()
	r := &receiver{answer: answer}
	r.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.reqs = append(r.reqs, req)
		r.body = append(r.body, b)
		n := len(r.reqs)
		r.mu.Unlock()
		code := http.StatusNoContent
		if len(r.answer) > 0 {
			code = r.answer[min(n, len(r.answer))-1]
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(r.srv.Certificate())
	return p
}

func loopbackOK(a netip.Addr) error {
	if a.Unmap().IsLoopback() {
		return nil
	}
	return safehttp.PublicOnly(a)
}

func sender(t *testing.T, pool *x509.CertPool, addrs func(netip.Addr) error) *Sender {
	t.Helper()
	return &Sender{Client: safehttp.New(safehttp.Config{AnyHost: true, RootCAs: pool, Addresses: addrs, Timeout: SendTimeout,
		MaxRedirects: 3}), UserAgent: "fapps-test", Now: func() time.Time { return time.Unix(1_800_000_000, 0) }}
}

func TestSendWebhook_GenericIsSignedAndCarriesTheOwnersHeader(t *testing.T) {
	rc := newReceiver(t)
	s := sender(t, rc.pool(), loopbackOK)
	hook := Webhook{ID: "w1", Name: "ops", Format: FormatGeneric, URL: rc.srv.URL + "/hook?x=1",
		Secret: []byte("whsec_test"), HeaderName: "Authorization", HeaderValue: "Bearer bn_abc"}
	res := s.SendWebhook(context.Background(), hook, sampleMessage, sampleMeta)
	if res.Status != StatusDelivered || res.HTTPStatus != http.StatusNoContent {
		t.Fatalf("result %+v", res)
	}
	req, body := rc.reqs[0], rc.body[0]
	if req.Method != http.MethodPost || req.URL.Path != "/hook" || req.URL.Query().Get("x") != "1" {
		t.Errorf("request %s %s", req.Method, req.URL)
	}
	if req.Header.Get("Authorization") != "Bearer bn_abc" || req.Header.Get("Content-Type") != "application/json" ||
		req.Header.Get(HeaderEvent) != KindRejected || req.Header.Get(HeaderDelivery) != "42" || req.Header.Get("User-Agent") != "fapps-test" {
		t.Errorf("headers %v", req.Header)
	}
	ts, _ := strconv.ParseInt(req.Header.Get(HeaderTimestamp), 10, 64)
	if ts != 1_800_000_000 || !Verify([]byte("whsec_test"), ts, body, req.Header.Get(HeaderSignature), time.Unix(ts, 0), time.Minute) {
		t.Errorf("signature %q over %s does not verify", req.Header.Get(HeaderSignature), body)
	}
	var g GenericBody
	if err := json.Unmarshal(body, &g); err != nil || g.Event != KindRejected || g.Title != sampleMessage.Title {
		t.Errorf("body %s", body)
	}
}

func TestSendWebhook_OtherFormatsAreNotSigned(t *testing.T) {
	rc := newReceiver(t)
	s := sender(t, rc.pool(), loopbackOK)
	for _, f := range []string{FormatSlack, FormatDiscord, FormatTeams} {
		res := s.SendWebhook(context.Background(), Webhook{Format: f, URL: rc.srv.URL}, sampleMessage, sampleMeta)
		if res.Status != StatusDelivered {
			t.Fatalf("%s: %+v", f, res)
		}
	}
	for i, req := range rc.reqs {
		if req.Header.Get(HeaderSignature) != "" || req.Header.Get(HeaderEvent) != "" {
			t.Errorf("request %d carries the generic headers: %v", i, req.Header)
		}
	}
}

// The SSRF rules with the default address rule: loopback, a private
// address, a name that resolves to loopback, plain http - none is called,
// and none is worth a retry.
func TestSendWebhook_RefusesWhatIsNotPublicHTTPS(t *testing.T) {
	rc := newReceiver(t)
	strict := sender(t, rc.pool(), nil)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(rc.srv.URL, "https://"))
	for name, addr := range map[string]string{
		"loopback":            rc.srv.URL,
		"localhost":           "https://localhost:" + port + "/",
		"private":             "https://10.0.0.1/hook",
		"link-local metadata": "https://169.254.169.254/latest/meta-data",
		"plain http":          "http://example.com/hook",
	} {
		res := strict.SendWebhook(context.Background(), Webhook{Format: FormatGeneric, URL: addr}, sampleMessage, sampleMeta)
		if res.Status != StatusFailed || res.Retry || res.HTTPStatus != 0 {
			t.Errorf("%s: %+v", name, res)
		}
		if strings.Contains(res.Error, "10.0.0.1") || strings.Contains(res.Error, "/hook") || strings.Contains(res.Error, "meta-data") {
			t.Errorf("%s: the error names the address: %q", name, res.Error)
		}
	}
	if len(rc.reqs) != 0 {
		t.Fatalf("the receiver was called %d times", len(rc.reqs))
	}
}

// A public receiver that redirects to a private address: the redirect is
// refused when the store dials it (127.0.0.1 stands for the public host,
// 127.0.0.2 for the private one).
func TestSendWebhook_RefusesARedirectToAPrivateAddress(t *testing.T) {
	var private int
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { private++ }))
	t.Cleanup(target.Close)
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(target.URL, "https://"))
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://127.0.0.2:"+port+"/internal", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)
	pool := x509.NewCertPool()
	pool.AddCert(redirect.Certificate())
	pool.AddCert(target.Certificate())
	only1 := func(a netip.Addr) error {
		if a.Unmap() == netip.MustParseAddr("127.0.0.1") {
			return nil
		}
		return safehttp.ErrPrivateAddress
	}
	res := sender(t, pool, only1).SendWebhook(context.Background(), Webhook{Format: FormatSlack, URL: redirect.URL}, sampleMessage, sampleMeta)
	if res.Status != StatusFailed || res.Retry || !strings.Contains(res.Error, "private") {
		t.Fatalf("result %+v", res)
	}
	if private != 0 {
		t.Fatal("the private address was called")
	}
}

func TestSendWebhook_WhatIsWorthARetry(t *testing.T) {
	for code, retry := range map[int]bool{500: true, 502: true, 503: true, 429: true, 408: true, 400: false, 401: false, 403: false, 404: false, 410: false} {
		rc := newReceiver(t, code)
		res := sender(t, rc.pool(), loopbackOK).SendWebhook(context.Background(), Webhook{Format: FormatDiscord, URL: rc.srv.URL}, sampleMessage, sampleMeta)
		if res.Status != StatusFailed || res.HTTPStatus != code || res.Retry != retry || res.Gone {
			t.Errorf("HTTP %d: %+v", code, res)
		}
	}
	// No answer at all (nothing listens): worth a retry.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	res := sender(t, nil, loopbackOK).SendWebhook(context.Background(), Webhook{Format: FormatDiscord, URL: "https://" + addr + "/"}, sampleMessage, sampleMeta)
	if res.Status != StatusFailed || !res.Retry {
		t.Errorf("connection refused: %+v", res)
	}
}

func TestSendWebhook_ASlowReceiverTimesOut(t *testing.T) {
	block := make(chan struct{})
	slow := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	t.Cleanup(func() { close(block); slow.Close() })
	pool := x509.NewCertPool()
	pool.AddCert(slow.Certificate())
	s := &Sender{Client: safehttp.New(safehttp.Config{AnyHost: true, RootCAs: pool, Addresses: loopbackOK, Timeout: 300 * time.Millisecond})}
	start := time.Now()
	res := s.SendWebhook(context.Background(), Webhook{Format: FormatSlack, URL: slow.URL}, sampleMessage, sampleMeta)
	if res.Status != StatusFailed || !res.Retry || time.Since(start) > 5*time.Second {
		t.Fatalf("result %+v after %s", res, time.Since(start))
	}
}

func TestSendPush_EncryptsSignsAndForgetsAGoneSubscription(t *testing.T) {
	pub, priv, _ := GenerateVAPID()
	v, _ := ParseVAPID(pub, priv, "https://apps.example")
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	rc := newReceiver(t, http.StatusCreated, http.StatusGone)
	var target PushTarget
	target.Endpoint = rc.srv.URL + "/push/abc"
	target.Keys.P256dh, target.Keys.Auth = b64.EncodeToString(ua.PublicKey().Bytes()), b64.EncodeToString(auth)
	s := sender(t, rc.pool(), loopbackOK)
	res := s.SendPush(context.Background(), v, target, sampleMessage, "submission.rejected:7")
	if res.Status != StatusDelivered || res.HTTPStatus != http.StatusCreated {
		t.Fatalf("result %+v", res)
	}
	req := rc.reqs[0]
	if req.Header.Get("Content-Encoding") != "aes128gcm" || req.Header.Get("TTL") == "" ||
		!strings.HasPrefix(req.Header.Get("Authorization"), "vapid t=") {
		t.Errorf("headers %v", req.Header)
	}
	plain, err := DecryptPush(rc.body[0], ua, auth)
	if err != nil {
		t.Fatal(err)
	}
	var p PushPayload
	if err := json.Unmarshal(plain, &p); err != nil || p.Title != sampleMessage.Title || p.URL != sampleMessage.URL || p.Tag != "submission.rejected:7" {
		t.Fatalf("payload %s", plain)
	}
	res = s.SendPush(context.Background(), v, target, sampleMessage, "t")
	if res.Status != StatusFailed || !res.Gone || res.Retry {
		t.Fatalf("410: %+v", res)
	}
	if res := s.SendPush(context.Background(), nil, target, sampleMessage, "t"); res.Status != StatusFailed {
		t.Fatalf("without a VAPID key: %+v", res)
	}
}
