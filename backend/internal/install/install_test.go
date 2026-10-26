package install

import (
	"net/url"
	"testing"
)

func TestNormalizeFilexURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://files.example.com":         "https://files.example.com",
		"https://Files.Example.COM/":        "https://files.example.com",
		"  https://files.example.com:443  ": "https://files.example.com",
		"https://files.example.com:8443":    "https://files.example.com:8443",
		"HTTPS://files.example.com":         "https://files.example.com",
		"https://192.0.2.5":                 "https://192.0.2.5",
		"https://[2001:db8::1]:9443/":       "https://[2001:db8::1]:9443",
		"https://xn--fles-6qa.example.com":  "https://xn--fles-6qa.example.com",
	} {
		got, err := NormalizeFilexURL(in, false)
		if err != nil || got != want {
			t.Errorf("%q -> %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"", "files.example.com", "http://files.example.com", "ftp://files.example.com", "https://",
		"https://files.example.com/admin", "https://files.example.com/?a=1", "https://files.example.com/#x",
		"https://files.example.com?", "https://user:pw@files.example.com", "https://files.example.com:0",
		"https://files.example.com:99999", "https://files.example.com:08443", "https://fil es.example.com",
		"https://files.example.com\\@evil", "javascript:alert(1)", "https://<script>.example", "https://-x.example",
		"http://localhost:5212",
	} {
		if got, err := NormalizeFilexURL(bad, false); err == nil {
			t.Errorf("%q accepted as %q", bad, got)
		}
	}
	for in, want := range map[string]string{
		"http://localhost:5212":   "http://localhost:5212",
		"http://127.0.0.1:80/":    "http://127.0.0.1",
		"http://[::1]:5212":       "http://[::1]:5212",
		"https://localhost:5212/": "https://localhost:5212",
	} {
		if got, err := NormalizeFilexURL(in, true); err != nil || got != want {
			t.Errorf("development %q -> %q %v, want %q", in, got, err, want)
		}
	}
	if _, err := NormalizeFilexURL("http://files.example.com", true); err == nil {
		t.Error("development lets any http host through")
	}
}

func TestMagicLinkIsTheContractsShape(t *testing.T) {
	token := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJ-_01234"
	link := MagicLink("https://files.example.com", "https://apps.example.com", token)
	if link != "https://files.example.com/admin/store-install#store=https%3A%2F%2Fapps.example.com&intent="+token {
		t.Fatalf("link = %s", link)
	}
	u, err := url.Parse(link)
	if err != nil || u.Path != "/admin/store-install" || u.RawQuery != "" {
		t.Fatalf("parsed %+v %v", u, err)
	}
	q, _ := url.ParseQuery(u.Fragment)
	if q.Get("store") != "https://apps.example.com" || q.Get("intent") != token {
		t.Fatalf("fragment %q", u.Fragment)
	}
}

func TestEncodeURIComponentIsJavaScripts(t *testing.T) {
	for in, want := range map[string]string{
		"https://apps.example.com:8443/x": "https%3A%2F%2Fapps.example.com%3A8443%2Fx",
		"a b&c=d?e#f":                     "a%20b%26c%3Dd%3Fe%23f",
		"-_.!~*'()":                       "-_.!~*'()",
		"ş":                               "%C5%9F",
	} {
		if got := EncodeURIComponent(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}
