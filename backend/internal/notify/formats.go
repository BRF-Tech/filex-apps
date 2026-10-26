package notify

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Webhook formats: the body a receiver is sent, chosen per webhook.
const (
	// FormatGeneric is the store's own JSON, signed with HMAC-SHA256
	// (X-Fapps-Signature), with one optional header of the owner's (an API
	// key). Its title, message, severity and action_url read as most
	// notification services' "send" call does, so such a service can take
	// the body as it is.
	FormatGeneric = "generic"
	// FormatSlack is a Slack incoming webhook's message (text + blocks).
	FormatSlack = "slack"
	// FormatDiscord is a Discord webhook's message (one embed, no mentions).
	FormatDiscord = "discord"
	// FormatTeams is a Microsoft Teams webhook's message (an Adaptive Card,
	// as a Workflows "When a Teams webhook request is received" flow and the
	// older incoming webhook take it).
	FormatTeams = "teams"
)

// Formats in the order the page offers them.
var Formats = []string{FormatGeneric, FormatSlack, FormatDiscord, FormatTeams}

// ValidFormat reports a known format.
func ValidFormat(f string) bool {
	for _, x := range Formats {
		if x == f {
			return true
		}
	}
	return false
}

// Headers of the generic format.
const (
	HeaderEvent     = "X-Fapps-Event"
	HeaderDelivery  = "X-Fapps-Delivery"
	HeaderTimestamp = "X-Fapps-Timestamp"
	HeaderSignature = "X-Fapps-Signature"
)

// Meta is what a delivery says about itself besides the message.
type Meta struct {
	// Delivery is the delivery's id ("test-<n>" for a test): the same on
	// every retry, so a receiver can drop a repeat.
	Delivery   string
	Event      string // the kind
	OccurredAt time.Time
	// Source is the store's host (apps.example.com).
	Source string
	// Store is the store's name (FAPPS_STORE_NAME), the footer of a Slack,
	// Discord or Teams message and a Discord message's sender name; "" or
	// the same as Source: the host alone.
	Store string
	// Data is the event's data (generic only).
	Data json.RawMessage
}

// GenericBody is the generic format's JSON (docs/ARCHITECTURE.md
// "Notifications", the body table).
type GenericBody struct {
	ID         string          `json:"id"`
	Event      string          `json:"event"`
	OccurredAt string          `json:"occurred_at"`
	Source     string          `json:"source"`
	Title      string          `json:"title"`
	Message    string          `json:"message"`
	Severity   string          `json:"severity"`
	ActionURL  string          `json:"action_url"`
	Data       json.RawMessage `json:"data"`
}

// footer is a message's footer line: the store's name and host, or the
// host alone when the name is the host (the default) or unknown.
func footer(meta Meta) string {
	name := strings.TrimSpace(meta.Store)
	switch {
	case name == "" || strings.EqualFold(name, meta.Source):
		return meta.Source
	case meta.Source == "":
		return name
	}
	return name + " · " + meta.Source
}

// discordUsername is the store's name as a Discord webhook's sender name, or
// "" to keep the webhook's own: Discord refuses (400) a name over 80
// characters, one containing "discord" or "clyde", and "everyone" or "here".
func discordUsername(name string) string {
	name = strings.TrimSpace(name)
	lower := strings.ToLower(name)
	if name == "" || strings.Contains(lower, "discord") || strings.Contains(lower, "clyde") || lower == "everyone" || lower == "here" {
		return ""
	}
	return strings.TrimSpace(clip(name, 80))
}

// Body is the request body of a webhook in its format.
func Body(format string, m Message, meta Meta) ([]byte, error) {
	switch format {
	case FormatGeneric:
		data := meta.Data
		if len(data) == 0 {
			data = json.RawMessage(`{}`)
		}
		return marshal(GenericBody{ID: meta.Delivery, Event: meta.Event, OccurredAt: meta.OccurredAt.UTC().Format(time.RFC3339),
			Source: meta.Source, Title: m.Title, Message: m.Text, Severity: m.Severity, ActionURL: m.URL, Data: data})
	case FormatSlack:
		title := slackEscape(m.Title)
		if m.URL != "" {
			title = "<" + slackEscape(m.URL) + "|" + strings.ReplaceAll(title, "|", "¦") + ">"
		}
		return marshal(map[string]any{
			"text": slackEscape(m.Title + ": " + m.Text),
			"blocks": []any{
				map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": "*" + title + "*\n" + slackEscape(m.Text)}},
				map[string]any{"type": "context", "elements": []any{map[string]any{"type": "mrkdwn", "text": slackEscape(footer(meta))}}},
			},
		})
	case FormatDiscord:
		embed := map[string]any{
			"title":       clip(m.Title, 256),
			"description": clip(m.Text, 4000),
			"color":       colour(m.Severity),
			"timestamp":   meta.OccurredAt.UTC().Format(time.RFC3339),
			"footer":      map[string]any{"text": footer(meta)},
		}
		if m.URL != "" {
			embed["url"] = m.URL
		}
		msg := map[string]any{
			// No @everyone, @here, role or user mention from an app's or a
			// publisher's name.
			"allowed_mentions": map[string]any{"parse": []string{}},
			"embeds":           []any{embed},
		}
		if u := discordUsername(meta.Store); u != "" {
			msg["username"] = u
		}
		return marshal(msg)
	case FormatTeams:
		card := map[string]any{
			"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
			"type":    "AdaptiveCard",
			"version": "1.4",
			"body": []any{
				map[string]any{"type": "TextBlock", "text": m.Title, "weight": "Bolder", "size": "Medium", "wrap": true,
					"color": teamsColour(m.Severity)},
				map[string]any{"type": "TextBlock", "text": m.Text, "wrap": true},
				map[string]any{"type": "TextBlock", "text": footer(meta), "isSubtle": true, "size": "Small", "wrap": true},
			},
		}
		if m.URL != "" {
			card["actions"] = []any{map[string]any{"type": "Action.OpenUrl", "title": "Open", "url": m.URL}}
		}
		return marshal(map[string]any{
			"type": "message",
			"attachments": []any{map[string]any{
				"contentType": "application/vnd.microsoft.card.adaptive",
				"contentUrl":  nil,
				"content":     card,
			}},
		})
	}
	return nil, fmt.Errorf("unknown webhook format %q", format)
}

// marshal is json.Marshal without HTML escaping (a receiver shows "<" and
// "&" as they are).
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// slackEscape escapes the three characters Slack's mrkdwn reads as markup.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func colour(severity string) int {
	switch severity {
	case SeveritySuccess:
		return 0x2e7d32
	case SeverityWarning:
		return 0xb26a00
	case SeverityDanger:
		return 0xc62828
	}
	return 0x1565c0
}

func teamsColour(severity string) string {
	switch severity {
	case SeveritySuccess:
		return "Good"
	case SeverityWarning:
		return "Warning"
	case SeverityDanger:
		return "Attention"
	}
	return "Default"
}

// Sign is the generic format's X-Fapps-Signature: "sha256=" and the
// lower-hex HMAC-SHA256, keyed with the webhook's secret, of the timestamp
// (decimal Unix seconds, X-Fapps-Timestamp), a full stop and the body.
func Sign(secret []byte, timestamp int64, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(strconv.FormatInt(timestamp, 10)))
	m.Write([]byte("."))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// Verify checks a signature the way a receiver should: the same HMAC, in
// constant time, and a timestamp within tolerance of now.
func Verify(secret []byte, timestamp int64, body []byte, signature string, now time.Time, tolerance time.Duration) bool {
	t := time.Unix(timestamp, 0)
	if t.Before(now.Add(-tolerance)) || t.After(now.Add(tolerance)) {
		return false
	}
	return hmac.Equal([]byte(Sign(secret, timestamp, body)), []byte(signature))
}

// forbiddenHeaders are names a webhook's own header may not take: the
// request's framing, the store's own signature headers, cookies.
var forbiddenHeaders = map[string]bool{
	"host": true, "content-length": true, "content-type": true, "content-encoding": true, "transfer-encoding": true,
	"connection": true, "keep-alive": true, "te": true, "trailer": true, "upgrade": true, "expect": true,
	"cookie": true, "user-agent": true, "proxy-authorization": true,
}

// ValidHeaderName reports a name a webhook's own header may take: an HTTP
// token, not one of the request's own and not X-Fapps-*.
func ValidHeaderName(name string) bool {
	if name == "" || len(name) > 64 || forbiddenHeaders[strings.ToLower(name)] || strings.HasPrefix(strings.ToLower(name), "x-fapps-") {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
			return false
		}
	}
	return http.CanonicalHeaderKey(name) != ""
}

// ValidHeaderValue reports a value a header may carry: printable, no line
// break, at most 1024 bytes.
func ValidHeaderValue(v string) bool {
	if v == "" || len(v) > 1024 {
		return false
	}
	for _, r := range v {
		if r < ' ' && r != '\t' || r == 0x7f {
			return false
		}
	}
	return true
}
