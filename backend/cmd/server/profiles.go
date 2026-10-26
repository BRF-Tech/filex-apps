package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"log/slog"
	"net/url"

	"golang.org/x/image/webp"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	ghapi "github.com/brf-tech/filex-apps/backend/internal/github"
	"github.com/brf-tech/filex-apps/backend/internal/media"
	"github.com/brf-tech/filex-apps/backend/internal/pipeline"
	"github.com/brf-tech/filex-apps/backend/internal/safehttp"
)

// avatarHost is the only place a GitHub avatar is mirrored from (it is on
// the default outbound allow list, config.DefaultAllowHosts).
const avatarHost = "avatars.githubusercontent.com"

// Avatar bounds: square, as GitHub serves them; asked at 256 px.
const (
	avatarSize    = 256
	avatarMinSide = 32
	avatarMaxSide = 1024
)

// githubProfiles implements pipeline.ProfileReader on the GitHub reader:
// the owner's profile text (`bio`, an organization's description too) and
// its avatar, mirrored through the media sink after the same checks as a
// listing image (sniffed type, declared size, a full decode).
type githubProfiles struct {
	gh   *ghapi.Client
	http *safehttp.Client
	sink pipeline.MediaSink
	max  int64
	log  *slog.Logger
}

var _ pipeline.ProfileReader = (*githubProfiles)(nil)

// Profile reads the owner by login and checks it is still the bound
// numeric id (a login can change hands). An avatar that cannot be mirrored
// is left out, not an error.
func (p *githubProfiles) Profile(ctx context.Context, ownerID int64, login string) (pipeline.Profile, error) {
	o, err := p.gh.Owner(ctx, "", login)
	if err != nil {
		return pipeline.Profile{}, err
	}
	if o.ID != ownerID {
		return pipeline.Profile{}, fmt.Errorf("the GitHub login %s belongs to id %d now, the publisher is bound to %d", login, o.ID, ownerID)
	}
	out := pipeline.Profile{Description: o.Bio}
	if o.AvatarURL != "" {
		sha, err := p.mirrorAvatar(ctx, o.AvatarURL)
		if err != nil {
			p.log.Warn("publisher avatar not mirrored", "login", login, "err", err)
		} else {
			out.AvatarSHA256 = sha
		}
	}
	return out, nil
}

func (p *githubProfiles) mirrorAvatar(ctx context.Context, raw string) (string, error) {
	u, err := avatarURL(raw)
	if err != nil {
		return "", err
	}
	b, err := p.http.Get(ctx, u, p.max, nil)
	if err != nil {
		return "", err
	}
	ref, err := checkAvatar(b.Bytes)
	if err != nil {
		return "", err
	}
	ref.SHA256 = b.SHA256
	if err := p.sink.Put(ctx, ref, u, b.Bytes); err != nil {
		return "", err
	}
	return b.SHA256, nil
}

// avatarURL keeps GitHub's avatar address on its own host and asks for the
// 256 px size.
func avatarURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != avatarHost || u.User != nil {
		return "", fmt.Errorf("the avatar address %q is not on https://%s", raw, avatarHost)
	}
	q := u.Query()
	q.Set("s", fmt.Sprint(avatarSize))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// checkAvatar is a square PNG, JPEG or WebP within bounds that decodes to
// the size it declares.
func checkAvatar(b []byte) (domain.MediaRef, error) {
	typ, w, h, err := media.Inspect(b)
	if err != nil {
		return domain.MediaRef{}, err
	}
	if w != h || w < avatarMinSide || w > avatarMaxSide {
		return domain.MediaRef{}, fmt.Errorf("an avatar is square, %d to %d px; this one is %dx%d", avatarMinSide, avatarMaxSide, w, h)
	}
	var img image.Image
	switch typ {
	case media.PNG:
		img, err = png.Decode(bytes.NewReader(b))
	case media.JPEG:
		img, err = jpeg.Decode(bytes.NewReader(b))
	case media.WebP:
		img, err = webp.Decode(bytes.NewReader(b))
	}
	if err != nil {
		return domain.MediaRef{}, fmt.Errorf("the avatar does not decode: %v", err)
	}
	if bb := img.Bounds(); bb.Dx() != w || bb.Dy() != h {
		return domain.MediaRef{}, fmt.Errorf("the avatar decodes to %dx%d, not the %dx%d it declares", bb.Dx(), bb.Dy(), w, h)
	}
	return domain.MediaRef{Role: "avatar", Type: typ, Width: w, Height: h}, nil
}
