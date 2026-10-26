// Package webui serves the Vue SPA (web/) that the image build copies into
// dist/. In a checkout dist/ holds only a placeholder, so `go build ./...`
// works without a web build and the handler answers 503 for the SPA.
package webui

import (
	"embed"
	"encoding/json"
	"html"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// fixedTypes are the media types of the files the SPA keeps at fixed,
// unhashed addresses (web/public: the favicon, the touch icon, the web app
// manifest), written out rather than left to the extension table: the
// runtime image has no /etc/mime.types, so Go knows neither .ico nor
// .webmanifest and would sniff (a manifest as text/plain).
var fixedTypes = map[string]string{
	".ico":         "image/x-icon",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".webmanifest": "application/manifest+json",
	".js":          "text/javascript; charset=utf-8",
}

// Options are what the server writes into the SPA's own files.
type Options struct {
	// Name is the store's name (FAPPS_STORE_NAME): index.html's <title> and
	// <meta name="application-name"> (which the interface reads at start,
	// so the brand never flashes another name), and the web app manifest's
	// name and short_name. Empty: the files as built.
	Name string
}

// Handler serves the SPA: a real file when the path names one, else
// index.html (client-side routes). Hashed assets are cached for a year,
// the fixed-address files (icons, manifest) for a day, index.html never.
func Handler(o Options) http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return serve(sub, o)
}

var (
	titleRe   = regexp.MustCompile(`(?s)<title>.*?</title>`)
	appNameRe = regexp.MustCompile(`<meta name="application-name" content="[^"]*"`)
)

// branded is index.html with the store's name in its title and its
// application-name.
func branded(index []byte, name string) []byte {
	if name == "" {
		return index
	}
	esc := html.EscapeString(name)
	out := titleRe.ReplaceAllLiteral(index, []byte("<title>"+esc+"</title>"))
	return appNameRe.ReplaceAllLiteral(out, []byte(`<meta name="application-name" content="`+esc+`"`))
}

// brandedManifest is the web app manifest with the store's name ("" and nil
// when there is nothing to change or it does not parse: the file as built).
func brandedManifest(b []byte, name string) []byte {
	if name == "" || b == nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	m["name"], m["short_name"] = name, name
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil
	}
	return append(out, '\n')
}

func serve(sub fs.FS, o Options) http.Handler {
	files := http.FileServer(http.FS(sub))
	index, statErr := fs.ReadFile(sub, "index.html")
	built := statErr == nil
	index = branded(index, o.Name)
	manifestRaw, _ := fs.ReadFile(sub, "manifest.webmanifest")
	manifest := brandedManifest(manifestRaw, o.Name)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !built {
			http.Error(w, "the web interface is not built into this binary", http.StatusServiceUnavailable)
			return
		}
		p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if p != "" && p != "index.html" {
			if st, err := fs.Stat(sub, p); err == nil && !st.IsDir() {
				if p == "manifest.webmanifest" && manifest != nil {
					w.Header().Set("Cache-Control", "public, max-age=86400")
					w.Header().Set("Content-Type", fixedTypes[".webmanifest"])
					if r.Method != http.MethodHead {
						_, _ = w.Write(manifest)
					}
					return
				}
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else if p == "sw.js" {
					// The service worker (browser notifications): the browser
					// checks it for an update on every visit.
					w.Header().Set("Cache-Control", "no-cache")
					w.Header().Set("Content-Type", fixedTypes[".js"])
				} else {
					// Same address when the file changes: a day, not a year.
					w.Header().Set("Cache-Control", "public, max-age=86400")
					if ct, ok := fixedTypes[path.Ext(p)]; ok {
						w.Header().Set("Content-Type", ct)
					}
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}
