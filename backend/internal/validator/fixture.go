package validator

import (
	"strings"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
)

// FixtureBody is an install body for TEST validators that read recorded
// fixtures instead of GitHub: the URL form of filex's install, pointing at a
// fixture server on the validator's own loopback (FILEX_PLUGIN_LOOPBACK_SOURCES=1
// on test validators only; filex's GitHub form always reads
// raw.githubusercontent.com and cannot be pointed elsewhere). The server
// answers
//
//	<base>/<owner>/<name>/<tag>/filex-app.json   the manifest, byte for byte
//	<base>/<owner>/<name>/<tag>/module           the module, for an app with one
//
// The module is pinned by the manifest's own wasm.sha256, as in production:
// the store's expectation is NOT sent, so a swapped module is filex's own
// sha256_mismatch. An interface bundle is fetched from the address the
// manifest names (a GitHub release for every real app), so interface-only
// apps need GitHub or a host mapping (the end-to-end stack's sidecars).
// Production uses GitHubBody.
func FixtureBody(base string) func(domain.DryRunRequest) any {
	base = strings.TrimRight(base, "/")
	return func(q domain.DryRunRequest) any {
		at := base + "/" + strings.Trim(q.Repo, "/") + "/" + q.Ref
		body := map[string]string{"manifest_url": at + "/filex-app.json", "url": ""}
		if q.ExpectWasmSHA256 != "" {
			body["url"] = at + "/module"
		}
		return body
	}
}
