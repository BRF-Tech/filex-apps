package domain

import "strings"

// NormalizePermissions writes permission ids the way filex's ParsePermission
// does before it lists them (trimmed; the host of `http:` lower-cased),
// without duplicates, in the given order. One rule for the resolver (a
// manifest's `permissions`), the validator's comparison and the index (a
// version's review rows): it lives here, below all three.
func NormalizePermissions(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if h, ok := strings.CutPrefix(id, "http:"); ok {
			id = "http:" + strings.ToLower(h)
		}
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
