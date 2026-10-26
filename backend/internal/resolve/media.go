package resolve

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"

	"golang.org/x/image/webp"
)

// Image rules (docs/ARCHITECTURE.md "Media mirror"; the consent schema's
// icon and screenshot descriptions). No SVG: it can carry script.
const (
	IconMin       = 128
	IconMax       = 1024
	ShotMinSide   = 320
	ShotMaxSide   = 3840
	ShotMaxPixels = 3840 * 2160
)

// imageType is a sniffed image kind.
type imageType struct {
	mime string
	ext  string
	cfg  func([]byte) (image.Config, error)
	dec  func([]byte) (image.Image, error)
}

var (
	typePNG  = imageType{"image/png", "png", cfgOf(png.DecodeConfig), decOf(png.Decode)}
	typeJPEG = imageType{"image/jpeg", "jpg", cfgOf(jpeg.DecodeConfig), decOf(jpeg.Decode)}
	typeWebP = imageType{"image/webp", "webp", cfgOf(webp.DecodeConfig), decOf(webp.Decode)}
)

func cfgOf(f func(io.Reader) (image.Config, error)) func([]byte) (image.Config, error) {
	return func(b []byte) (image.Config, error) { return f(bytes.NewReader(b)) }
}

func decOf(f func(io.Reader) (image.Image, error)) func([]byte) (image.Image, error) {
	return func(b []byte) (image.Image, error) { return f(bytes.NewReader(b)) }
}

// sniff names the type by its magic bytes, never by the file name or a
// Content-Type header.
func sniff(b []byte) (imageType, bool) {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return typePNG, true
	case bytes.HasPrefix(b, []byte("\xff\xd8\xff")):
		return typeJPEG, true
	case len(b) >= 12 && bytes.Equal(b[0:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return typeWebP, true
	}
	return imageType{}, false
}

// checkedImage is an image that passed the rules.
type checkedImage struct {
	typ           imageType
	width, height int
}

// checkImage sniffs, reads the declared size, checks the bounds for the role
// and only then decodes the whole image (so a header that lies about its size
// cannot make the decoder allocate more than the bounds allow), to prove the
// bytes are the type they claim.
func checkImage(role string, b []byte) (*checkedImage, error) {
	t, ok := sniff(b)
	if !ok {
		return nil, fmt.Errorf("not a PNG, JPEG or WebP image")
	}
	if role == roleIcon && t.mime == typeJPEG.mime {
		return nil, fmt.Errorf("an icon is a PNG or WebP (a JPEG has no transparency)")
	}
	cfg, err := t.cfg(b)
	if err != nil {
		return nil, fmt.Errorf("unreadable %s: %v", t.ext, err)
	}
	w, h := cfg.Width, cfg.Height
	switch role {
	case roleIcon:
		if w != h {
			return nil, fmt.Errorf("an icon is square; this one is %dx%d", w, h)
		}
		if w < IconMin || w > IconMax {
			return nil, fmt.Errorf("an icon is %d to %d px; this one is %d", IconMin, IconMax, w)
		}
	default:
		if w < ShotMinSide || h < ShotMinSide || w > ShotMaxSide || h > ShotMaxSide || w*h > ShotMaxPixels {
			return nil, fmt.Errorf("a screenshot is %d to %d px a side and at most %d pixels; this one is %dx%d",
				ShotMinSide, ShotMaxSide, ShotMaxPixels, w, h)
		}
	}
	img, err := t.dec(b)
	if err != nil {
		return nil, fmt.Errorf("the %s does not decode: %v", t.ext, err)
	}
	if bb := img.Bounds(); bb.Dx() != w || bb.Dy() != h {
		return nil, fmt.Errorf("the %s decodes to %dx%d, not the %dx%d it declares", t.ext, bb.Dx(), bb.Dy(), w, h)
	}
	return &checkedImage{typ: t, width: w, height: h}, nil
}
