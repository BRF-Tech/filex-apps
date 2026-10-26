package markdown

import (
	"strings"
	"testing"
)

func TestRenderKeepsTextAndStructure(t *testing.T) {
	got := Render("# Title\n\nSome **bold** and `code`.\n\n- one\n- two\n\n| a | b |\n|:--|--:|\n| 1 | 2 |\n\n~~gone~~\n")
	for _, want := range []string{"<h1>Title</h1>", "<strong>bold</strong>", "<code>code</code>", "<li>one</li>",
		"<table>", `<th align="left">a</th>`, `<td align="right">2</td>`, "<del>gone</del>"} {
		if !strings.Contains(got, want) {
			t.Errorf("render lacks %s:\n%s", want, got)
		}
	}
}

func TestRenderRemovesWhatRunsAndKeepsTheText(t *testing.T) {
	cases := []struct {
		src    string
		banned []string
		keep   string
	}{
		{"<script>alert(1)</script>\n\nkept text", []string{"<script", "alert(1)</script"}, "kept text"},
		{"<p style=\"color:red\" onclick=\"x()\">hi</p>\n\nplain words", []string{"style=", "onclick"}, "plain words"},
		{"before\n\n<iframe src=\"https://evil\"></iframe>", []string{"<iframe"}, "before"},
		{"<form action=\"/x\"><input name=a></form>\n\nafter the form", []string{"<form", "<input"}, "after the form"},
		{"[click](javascript:alert(1))", []string{"javascript:"}, "click"},
		{"look ![pixel](https://tracker.example/p.png) here", []string{"<img", "tracker.example"}, "look"},
		{"<a href=\"https://x.example\" onmouseover=\"y\">x</a>\n\nlink above", []string{"onmouseover"}, "link above"},
		{"- [x] done", []string{"<input"}, "done"},
		{"<svg onload=alert(1)></svg>\n\nno picture", []string{"<svg", "onload"}, "no picture"},
	}
	for _, c := range cases {
		got := Render(c.src)
		for _, b := range c.banned {
			if strings.Contains(got, b) {
				t.Errorf("Render(%q) kept %q:\n%s", c.src, b, got)
			}
		}
		if !strings.Contains(got, c.keep) {
			t.Errorf("Render(%q) lost the text %q:\n%s", c.src, c.keep, got)
		}
	}
}

func TestLinksAreMarkedAndOpenOutside(t *testing.T) {
	got := Render("See [docs](https://docs.example/x) and https://auto.example/y")
	if !strings.Contains(got, `href="https://docs.example/x"`) {
		t.Fatalf("link lost: %s", got)
	}
	for _, want := range []string{"nofollow", "noreferrer", `target="_blank"`} {
		if !strings.Contains(got, want) {
			t.Errorf("link lacks %s: %s", want, got)
		}
	}
	if !strings.Contains(got, `href="https://auto.example/y"`) {
		t.Errorf("bare address not linked: %s", got)
	}
}

func TestRenderEmptyAndHuge(t *testing.T) {
	if Render("") != "" {
		t.Fatal("empty input must render empty")
	}
	big := strings.Repeat("a", MaxInput*2)
	got := Render(big)
	if len(got) > MaxInput+64 || !strings.Contains(got, strings.Repeat("a", 1000)) {
		t.Fatalf("output %d bytes; input is cut at %d and kept", len(got), MaxInput)
	}
}
