// Command mock-idp stands in for the outside world in the end-to-end stack
// (docker-compose.e2e.yml). TEST-ONLY: it signs in whoever presses a button
// and serves made-up repositories. It is never built into the store's image.
//
//	mock-idp serve        GitHub OAuth, GitHub's REST API, raw files and
//	                      release downloads (path-prefixed, see routes.go),
//	                      over HTTPS and plain HTTP on the compose network.
//	                      Creates the stack's throw-away CA and server
//	                      certificate on its first start (MOCK_CA_DIR).
//	mock-idp edge         The store's HTTPS front door: what a reverse proxy is
//	                      in front of a real store. Terminates TLS with the
//	                      throw-away certificate and proxies to the app with
//	                      X-Forwarded-Proto / -Host / -For, as a proxy does.
//	mock-idp github-tls   GitHub's own host names over HTTPS on 127.0.0.1:443,
//	                      inside another container's network namespace, for
//	                      programs that cannot be pointed elsewhere (filex's
//	                      GitHub install reads raw.githubusercontent.com).
//	mock-idp healthcheck <url>
//
// Settings come from MOCK_* environment variables (config.go).
package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mock-idp serve|edge|github-tls|healthcheck <url>")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(loadConfig())
	case "edge":
		err = runEdge(loadConfig())
	case "github-tls":
		err = runGitHubTLS(loadConfig())
	case "healthcheck":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: mock-idp healthcheck <url>")
			os.Exit(2)
		}
		os.Exit(healthcheck(os.Args[2]))
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		log.Fatal(err)
	}
}

// healthcheck GETs url (any certificate accepted: the stack's CA is made up)
// and answers 0 for a status below 400. The image has no shell or curl.
func healthcheck(url string) int {
	c := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // test-only probe
	}
	resp, err := c.Get(url)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		fmt.Fprintln(os.Stderr, "status", resp.StatusCode)
		return 1
	}
	return 0
}
