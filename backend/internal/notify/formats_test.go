package notify

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var sampleMessage = Message{
	Title:    "Not approved: hello 1.2.0",
	Text:     "The reviewer did not approve hello 1.2.0: <b>tests</b> & docs missing",
	URL:      "https://apps.example/submissions/7",
	Severity: SeverityWarning,
}

var sampleMeta = Meta{Delivery: "42", Event: KindRejected, OccurredAt: time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC),
	Source: "apps.example", Store: "Acme Apps", Data: json.RawMessage(`{"submission_id":7,"app":"hello"}`)}

func decodeBody(t *testing.T, format string) map[string]any {
	t.Helper()
	b, err := Body(format, sampleMessage, sampleMeta)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s: not JSON: %s", format, b)
	}
	return v
}

func TestBody_Generic(t *testing.T) {
	v := decodeBody(t, FormatGeneric)
	want := map[string]any{"id": "42", "event": "submission.rejected", "occurred_at": "2026-10-20T09:30:00Z",
		"source": "apps.example", "title": sampleMessage.Title, "message": sampleMessage.Text, "severity": "warning",
		"action_url": "https://apps.example/submissions/7"}
	for k, w := range want {
		if v[k] != w {
			t.Errorf("%s = %v, want %v", k, v[k], w)
		}
	}
	if d, ok := v["data"].(map[string]any); !ok || d["app"] != "hello" {
		t.Errorf("data = %v", v["data"])
	}
	// What a notification service's send call reads: title, message,
	// severity, action_url, never HTML-escaped.
	b, _ := Body(FormatGeneric, sampleMessage, sampleMeta)
	if strings.Contains(string(b), "u003c") || !strings.Contains(string(b), "<b>tests</b> & docs") {
		t.Errorf("HTML-escaped: %s", b)
	}
	// No data: an empty object, never null.
	m := sampleMeta
	m.Data = nil
	b, _ = Body(FormatGeneric, sampleMessage, m)
	if !strings.Contains(string(b), `"data":{}`) {
		t.Errorf("no data: %s", b)
	}
}

func TestBody_Slack(t *testing.T) {
	v := decodeBody(t, FormatSlack)
	text, _ := v["text"].(string)
	if !strings.Contains(text, "&lt;b&gt;tests&lt;/b&gt; &amp; docs") {
		t.Errorf("text not escaped for mrkdwn: %q", text)
	}
	blocks, _ := v["blocks"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("blocks %v", v["blocks"])
	}
	ctx := blocks[1].(map[string]any)["elements"].([]any)[0].(map[string]any)
	if ctx["text"] != "Acme Apps · apps.example" {
		t.Errorf("footer %v", ctx["text"])
	}
	section := blocks[0].(map[string]any)["text"].(map[string]any)
	if section["type"] != "mrkdwn" || !strings.HasPrefix(section["text"].(string), "*<https://apps.example/submissions/7|Not approved: hello 1.2.0>*\n") {
		t.Errorf("section %v", section)
	}
}

func TestBody_Discord(t *testing.T) {
	v := decodeBody(t, FormatDiscord)
	if v["username"] != "Acme Apps" {
		t.Errorf("username %v", v["username"])
	}
	am, _ := v["allowed_mentions"].(map[string]any)
	if parse, ok := am["parse"].([]any); !ok || len(parse) != 0 {
		t.Errorf("allowed_mentions %v: a name could ping @everyone", v["allowed_mentions"])
	}
	embeds, _ := v["embeds"].([]any)
	if len(embeds) != 1 {
		t.Fatalf("embeds %v", v["embeds"])
	}
	e := embeds[0].(map[string]any)
	if e["footer"].(map[string]any)["text"] != "Acme Apps · apps.example" {
		t.Errorf("footer %v", e["footer"])
	}
	if e["title"] != sampleMessage.Title || e["description"] != sampleMessage.Text || e["url"] != sampleMessage.URL ||
		e["timestamp"] != "2026-10-20T09:30:00Z" || int(e["color"].(float64)) != 0xb26a00 {
		t.Errorf("embed %v", e)
	}
	long := sampleMessage
	long.Title = strings.Repeat("t", 300)
	b, _ := Body(FormatDiscord, long, sampleMeta)
	var w struct{ Embeds []struct{ Title string } }
	_ = json.Unmarshal(b, &w)
	if n := len([]rune(w.Embeds[0].Title)); n != 256 {
		t.Errorf("title of %d runes; Discord refuses more than 256", n)
	}
}

func TestBody_Teams(t *testing.T) {
	v := decodeBody(t, FormatTeams)
	if v["type"] != "message" {
		t.Errorf("type %v", v["type"])
	}
	att := v["attachments"].([]any)[0].(map[string]any)
	if att["contentType"] != "application/vnd.microsoft.card.adaptive" {
		t.Errorf("contentType %v", att["contentType"])
	}
	card := att["content"].(map[string]any)
	if card["type"] != "AdaptiveCard" || card["version"] != "1.4" {
		t.Errorf("card %v", card)
	}
	body := card["body"].([]any)
	if body[0].(map[string]any)["text"] != sampleMessage.Title || body[0].(map[string]any)["color"] != "Warning" ||
		body[1].(map[string]any)["text"] != sampleMessage.Text || body[2].(map[string]any)["text"] != "Acme Apps · apps.example" {
		t.Errorf("card body %v", body)
	}
	action := card["actions"].([]any)[0].(map[string]any)
	if action["type"] != "Action.OpenUrl" || action["url"] != sampleMessage.URL {
		t.Errorf("action %v", action)
	}
}

// The footer is the store's name and host, or the host alone when the name
// is the host (the default); Discord's sender name is left to the webhook
// when Discord would refuse the store's.
func TestFooterAndTheDiscordSenderName(t *testing.T) {
	for _, tc := range []struct{ store, source, want string }{
		{"Acme Apps", "apps.example", "Acme Apps · apps.example"},
		{"apps.example", "apps.example", "apps.example"},
		{"Apps.Example", "apps.example", "apps.example"},
		{"", "apps.example", "apps.example"},
		{"Acme Apps", "", "Acme Apps"},
	} {
		if got := footer(Meta{Store: tc.store, Source: tc.source}); got != tc.want {
			t.Errorf("footer(%q, %q) = %q, want %q", tc.store, tc.source, got, tc.want)
		}
	}
	for in, want := range map[string]string{
		"Acme Apps":        "Acme Apps",
		"apps.example":     "apps.example",
		"My Discord store": "",
		"CLYDE apps":       "",
		"everyone":         "",
		"Here":             "",
		"":                 "",
	} {
		if got := discordUsername(in); got != want {
			t.Errorf("discordUsername(%q) = %q, want %q", in, got, want)
		}
	}
	for _, long := range []string{strings.Repeat("a", 100), strings.Repeat("é", 81)} {
		if got := discordUsername(long); got == "" || len([]rune(got)) > 80 {
			t.Errorf("discordUsername of %d runes = %d runes; Discord refuses more than 80", len([]rune(long)), len([]rune(got)))
		}
	}
	m := sampleMeta
	m.Store = "Discord fans"
	b, _ := Body(FormatDiscord, sampleMessage, m)
	if strings.Contains(string(b), `"username"`) {
		t.Errorf("a name Discord refuses was sent: %s", b)
	}
}

func TestBody_UnknownFormat(t *testing.T) {
	if _, err := Body("email", sampleMessage, sampleMeta); err == nil {
		t.Fatal("an unknown format made a body")
	}
	for _, f := range Formats {
		if !ValidFormat(f) {
			t.Errorf("%s not valid", f)
		}
	}
}

// The signature, checked against Python's hmac (an implementation apart
// from this one): hmac.new(b"whsec_test", b'1800000000.{"a":1}', sha256).
func TestSign_IsHMACSHA256OfTimestampDotBody(t *testing.T) {
	got := Sign([]byte("whsec_test"), 1_800_000_000, []byte(`{"a":1}`))
	if want := "sha256=7de5e6bbc99c65ef0ddccacd4308ba03a5a12eb8859a0db070c5ca731c4fdc19"; got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
	now := time.Unix(1_800_000_100, 0)
	if !Verify([]byte("whsec_test"), 1_800_000_000, []byte(`{"a":1}`), got, now, 5*time.Minute) {
		t.Fatal("its own signature does not verify")
	}
	for name, ok := range map[string]bool{
		"another body":   Verify([]byte("whsec_test"), 1_800_000_000, []byte(`{"a":2}`), got, now, 5*time.Minute),
		"another secret": Verify([]byte("whsec_other"), 1_800_000_000, []byte(`{"a":1}`), got, now, 5*time.Minute),
		"another time":   Verify([]byte("whsec_test"), 1_800_000_001, []byte(`{"a":1}`), got, now, 5*time.Minute),
		"too old":        Verify([]byte("whsec_test"), 1_800_000_000, []byte(`{"a":1}`), got, now.Add(time.Hour), 5*time.Minute),
	} {
		if ok {
			t.Errorf("%s verified", name)
		}
	}
}

func TestHeaderRules(t *testing.T) {
	for _, n := range []string{"Authorization", "X-Api-Key", "x-token", "Api_Key"} {
		if !ValidHeaderName(n) {
			t.Errorf("%q refused", n)
		}
	}
	for _, n := range []string{"", "Host", "content-type", "Content-Length", "Cookie", "X-Fapps-Signature", "X Key", "Key:", strings.Repeat("a", 65), "Kéy"} {
		if ValidHeaderName(n) {
			t.Errorf("%q accepted", n)
		}
	}
	if !ValidHeaderValue("Bearer bn_abc123") || ValidHeaderValue("a\r\nX-Evil: 1") || ValidHeaderValue("") || ValidHeaderValue(strings.Repeat("v", 1025)) {
		t.Error("header value rules")
	}
}
