package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

var icoBytes = []byte{0, 0, 1, 0, 1, 0, 16, 16, 0, 0, 1, 0, 32, 0}

func builtSPA() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>App store</title><meta name=\"application-name\" content=\"App store\" />")},
		"favicon.ico":          {Data: icoBytes},
		"favicon.svg":          {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"></svg>`)},
		"apple-touch-icon.png": {Data: []byte("\x89PNG\r\n\x1a\n")},
		"manifest.webmanifest": {Data: []byte(`{"name":"App store","short_name":"App store","icons":[{"src":"/icons/icon-192.png"}]}`)},
		"icons/icon-192.png":   {Data: []byte("\x89PNG\r\n\x1a\n")},
		"assets/index-abc.js":  {Data: []byte("console.log(1)")},
	}
}

func get(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

// The bug this guards: /favicon.ico answered 200 with the SPA's index.html,
// so the tab had no icon. A fixed-address file is the file, with its own
// type (the runtime image has no mime table) and a day of caching.
func TestFixedAddressFilesAreTheFilesNotTheSPA(t *testing.T) {
	h := serve(builtSPA(), Options{})
	for _, c := range []struct{ path, ctype string }{
		{"/favicon.ico", "image/x-icon"},
		{"/favicon.svg", "image/svg+xml"},
		{"/apple-touch-icon.png", "image/png"},
		{"/manifest.webmanifest", "application/manifest+json"},
		{"/icons/icon-192.png", "image/png"},
	} {
		for _, m := range []string{http.MethodGet, http.MethodHead} {
			rec := get(t, h, m, c.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s %s: status %d", m, c.path, rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != c.ctype {
				t.Errorf("%s %s: Content-Type %q, want %q", m, c.path, got, c.ctype)
			}
			if got := rec.Header().Get("Cache-Control"); got != "public, max-age=86400" {
				t.Errorf("%s %s: Cache-Control %q", m, c.path, got)
			}
		}
	}
	if rec := get(t, h, http.MethodGet, "/favicon.ico"); rec.Body.String() != string(icoBytes) {
		t.Errorf("/favicon.ico body is not the icon: %q", rec.Body.String())
	}
}

func TestHashedAssetsAreImmutableAndRoutesAreTheSPA(t *testing.T) {
	h := serve(builtSPA(), Options{})
	if got := get(t, h, http.MethodGet, "/assets/index-abc.js").Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("asset Cache-Control %q", got)
	}
	for _, p := range []string{"/", "/apps/filex-sign", "/index.html"} {
		rec := get(t, h, http.MethodGet, p)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" ||
			rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: %d %q %q, want the SPA shell", p, rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
		}
	}
}

func TestUnbuiltSPAAnswers503(t *testing.T) {
	rec := get(t, serve(fstest.MapFS{".gitkeep": {}}, Options{}), http.MethodGet, "/favicon.ico")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", rec.Code)
	}
}

// The service worker of the browser notifications: JavaScript, at the root
// (its scope is the whole store), never cached past a revalidation.
func TestServiceWorkerIsScriptAtTheRootAndRevalidated(t *testing.T) {
	spa := builtSPA()
	spa["sw.js"] = &fstest.MapFile{Data: []byte("self.addEventListener('push', () => {})")}
	rec := get(t, serve(spa, Options{}), http.MethodGet, "/sw.js")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/javascript; charset=utf-8" ||
		rec.Header().Get("Cache-Control") != "no-cache" || !strings.Contains(rec.Body.String(), "push") {
		t.Fatalf("sw.js: %d %q %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"), rec.Body.String())
	}
}

// The store's name reaches the page before any script runs: the title, the
// application-name the interface reads at start, and the web app manifest.
// It is HTML-escaped; without a name the files are served as built.
func TestTheStoreNameIsWrittenIntoTheShellAndTheManifest(t *testing.T) {
	h := serve(builtSPA(), Options{Name: `Acme <Apps> & "Co"`})
	for _, p := range []string{"/", "/apps/x", "/index.html"} {
		body := get(t, h, http.MethodGet, p).Body.String()
		want := `<title>Acme &lt;Apps&gt; &amp; &#34;Co&#34;</title><meta name="application-name" content="Acme &lt;Apps&gt; &amp; &#34;Co&#34;" />`
		if !strings.Contains(body, want) || strings.Contains(body, "App store") {
			t.Errorf("%s: %s", p, body)
		}
	}
	rec := get(t, h, http.MethodGet, "/manifest.webmanifest")
	var m struct {
		Name      string `json:"name"`
		ShortName string `json:"short_name"`
		Icons     []struct {
			Src string `json:"src"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest: %v %s", err, rec.Body.String())
	}
	if m.Name != `Acme <Apps> & "Co"` || m.ShortName != m.Name || len(m.Icons) != 1 || m.Icons[0].Src != "/icons/icon-192.png" {
		t.Errorf("manifest %+v", m)
	}
	if rec.Header().Get("Content-Type") != "application/manifest+json" || rec.Header().Get("Cache-Control") != "public, max-age=86400" {
		t.Errorf("manifest headers %v", rec.Header())
	}
	if head := get(t, h, http.MethodHead, "/manifest.webmanifest"); head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Errorf("HEAD manifest: %d %q", head.Code, head.Body.String())
	}

	plain := serve(builtSPA(), Options{})
	if body := get(t, plain, http.MethodGet, "/").Body.String(); !strings.Contains(body, "<title>App store</title>") {
		t.Errorf("no name: %s", body)
	}
	if body := get(t, plain, http.MethodGet, "/manifest.webmanifest").Body.String(); !strings.Contains(body, `"name":"App store"`) {
		t.Errorf("no name, manifest: %s", body)
	}
}
