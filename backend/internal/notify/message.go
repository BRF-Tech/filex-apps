package notify

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Severities, the generic body's `severity` (and the colour of a Slack,
// Discord or Teams message).
const (
	SeverityInfo    = "info"
	SeveritySuccess = "success"
	SeverityWarning = "warning"
	SeverityDanger  = "danger"
)

// Message is a notification in words: what every format and the browser
// show. The store is English only, so is every message.
type Message struct {
	Title    string
	Text     string
	URL      string // the store's page to act on (absolute)
	Severity string
}

// FilexReleaseURL is where a filex release's notes are (a link the reader
// follows; the store never reads it).
const FilexReleaseURL = "https://github.com/BRF-Tech/filex/releases/tag/v"

// Render writes the message of an event. base is the store's public URL,
// name the store's name (FAPPS_STORE_NAME; the host of base when empty).
// data is the event's data as stored; a field it lacks reads as a dash, so
// an older event never fails to render.
func Render(kind string, data json.RawMessage, base, name string) Message {
	var d map[string]any
	_ = json.Unmarshal(data, &d)
	f := func(k string) string { return field(d, k) }
	base = strings.TrimRight(base, "/")
	if name == "" {
		name = hostOf(base)
	}
	subURL := base + "/submissions/" + url.PathEscape(f("submission_id"))
	what := strings.TrimSpace(f("app") + " " + f("version"))
	if f("app") == "-" {
		what = f("repo") + " at " + f("ref")
	}
	by := f("publisher_name")
	if by == "-" {
		by = f("publisher")
	}
	switch kind {
	case KindAppSubmitted:
		return Message{
			Title:    "New app to review: " + what,
			Text:     fmt.Sprintf("%s submitted a new app, %s (%s at %s, %s). Its checks passed and it waits for review.", by, what, f("repo"), f("ref"), kindWord(f("kind"))),
			URL:      subURL,
			Severity: SeverityInfo,
		}
	case KindVersionSubmitted:
		return Message{
			Title:    "New version to review: " + what,
			Text:     fmt.Sprintf("%s submitted %s (%s at %s). Its checks passed and it waits for review.", by, what, f("repo"), f("ref")),
			URL:      subURL,
			Severity: SeverityInfo,
		}
	case KindPublisherRequested:
		owner := "GitHub user"
		if f("owner_type") == "Organization" {
			owner = "GitHub organization"
		}
		who := "Someone"
		if r := f("requested_by"); r != "-" {
			who = r
		}
		return Message{
			Title:    "Publisher to activate: " + by,
			Text:     fmt.Sprintf("%s asked for the publisher %s (%s), bound to the %s %s. It waits for an administrator to activate it.", who, by, f("publisher"), owner, f("github_owner")),
			URL:      base + "/admin/publishers",
			Severity: SeverityInfo,
		}
	case KindPipelineError:
		return Message{
			Title:    "A submission could not be checked: " + what,
			Text:     fmt.Sprintf("The store could not check %s (%s at %s) and failed it with %s: %s", what, f("repo"), f("ref"), f("code"), clip(f("message"), 600)),
			URL:      subURL,
			Severity: SeverityDanger,
		}
	case KindApproved:
		page := base + "/apps/" + url.PathEscape(f("app"))
		if v := f("version"); v != "-" {
			page += "/versions/" + url.PathEscape(v)
		}
		return Message{
			Title:    "Approved: " + what,
			Text:     fmt.Sprintf("%s is approved and listed in the store.", what),
			URL:      page,
			Severity: SeveritySuccess,
		}
	case KindRejected:
		text := fmt.Sprintf("The reviewer did not approve %s.", what)
		if r := f("reason"); r != "-" {
			text = fmt.Sprintf("The reviewer did not approve %s: %s", what, clip(r, 600))
		}
		return Message{Title: "Not approved: " + what, Text: text, URL: subURL, Severity: SeverityWarning}
	case KindFilexReleased:
		text := fmt.Sprintf("The store now knows filex %s and checks apps against it.", f("version"))
		if p := f("previous"); p != "-" && p != "" {
			text = fmt.Sprintf("The store now knows filex %s (before: %s) and checks apps against it.", f("version"), p)
		}
		return Message{
			Title:    "filex " + f("version") + " is out",
			Text:     text,
			URL:      FilexReleaseURL + url.PathEscape(f("version")),
			Severity: SeverityInfo,
		}
	case KindTest:
		return Message{
			Title:    "Test notification from " + name,
			Text:     fmt.Sprintf("This is a test of %s. If you read it, the channel works.", f("channel")),
			URL:      base + "/me/notifications",
			Severity: SeverityInfo,
		}
	}
	return Message{Title: name + ": " + kind, Text: "Something happened in the store.", URL: base + "/", Severity: SeverityInfo}
}

// hostOf is the host of an address ("" when it has none).
func hostOf(addr string) string {
	if u, err := url.Parse(addr); err == nil {
		return u.Host
	}
	return ""
}

func kindWord(k string) string {
	if k == "storage" {
		return "a storage plugin"
	}
	return "an app or a language pack"
}

// field is d[k] as text ("-" when missing or empty).
func field(d map[string]any, k string) string {
	v, ok := d[k]
	if !ok || v == nil {
		return "-"
	}
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		s = strconv.FormatBool(x)
	default:
		b, _ := json.Marshal(x)
		s = string(b)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	return s
}

// clip shortens s to at most n runes, with an ellipsis.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
