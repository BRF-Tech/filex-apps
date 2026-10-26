package httpapi

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// publisherListItemView is one row of GET /api/v1/publishers
// (PublisherListItem in the OpenAPI file).
type publisherListItemView struct {
	publisherRefView
	GitHubOwner string `json:"github_owner"`
	OwnerType   string `json:"owner_type"`
	Homepage    string `json:"homepage,omitempty"`
	Description string `json:"description,omitempty"`
	AvatarURL   string `json:"avatar_url,omitempty"`
	Apps        int    `json:"apps"`
}

type publisherPageView struct {
	Items   []publisherListItemView `json:"items"`
	Total   int                     `json:"total"`
	Page    int                     `json:"page"`
	PerPage int                     `json:"per_page"`
}

// avatarURL is the mirrored avatar's address, "" when the publisher has
// none.
func (h *handler) avatarURL(p store.Publisher) string {
	if p.AvatarSHA256 == "" || p.AvatarType == "" {
		return ""
	}
	return h.mediaURL(p.AvatarSHA256, p.AvatarType)
}

// listPublishers is the public publisher list: active publishers only, each
// with the number of its listed apps.
func (h *handler) listPublishers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, pp, err := page(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	text := strings.TrimSpace(q.Get("q"))
	if utf8.RuneCountInString(text) > 100 {
		h.fail(w, r, invalid("q is at most 100 characters."))
		return
	}
	sortBy := q.Get("sort")
	switch sortBy {
	case "", "name", "apps":
	default:
		h.fail(w, r, invalid("sort must be name or apps."))
		return
	}
	words := strings.Fields(text)
	if len(words) > 8 {
		words = words[:8]
	}
	rows, total, err := store.ListPublicPublishers(r.Context(), h.Pool, store.PublicPublisherFilter{Words: words, Sort: sortBy},
		store.Page{Page: p, PerPage: pp})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := publisherPageView{Items: []publisherListItemView{}, Total: total, Page: p, PerPage: pp}
	for _, x := range rows {
		out.Items = append(out.Items, publisherListItemView{
			publisherRefView: publisherRefView{ID: x.ID, Name: x.Name, Verified: x.Verified, Official: x.Official},
			GitHubOwner:      x.GitHubOwner, OwnerType: x.OwnerType, Homepage: x.Homepage,
			Description: x.Description, AvatarURL: h.avatarURL(x.Publisher), Apps: x.Apps,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
