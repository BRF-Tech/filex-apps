package index

import (
	"strings"

	"github.com/brf-tech/filex-apps/backend/internal/filexrange"
)

// CompareVersions orders two versions the way filex does
// (internal/filexrange, a port of filex's own update.Compare): the index's
// `latest` and the order of `versions` must be the ones filex's update
// notices will see. A version filex cannot parse sorts below every valid
// one, and two such compare as text.
func CompareVersions(a, b string) int {
	va, ea := filexrange.ParseVersion(a)
	vb, eb := filexrange.ParseVersion(b)
	switch {
	case ea != nil && eb != nil:
		return strings.Compare(a, b)
	case ea != nil:
		return -1
	case eb != nil:
		return 1
	}
	return va.Compare(vb)
}

// ValidVersion reports whether filex reads v as a version.
func ValidVersion(v string) bool {
	_, err := filexrange.ParseVersion(v)
	return err == nil
}
