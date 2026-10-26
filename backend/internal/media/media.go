// Package media keeps the mirrored images (icons, screenshots): it
// implements pipeline.MediaSink for the resolver, which fetched and checked
// them, and checks them once more before they are stored. The bytes live in
// Postgres (phase 1) and are served at /v1/media/<sha256>.<ext>.
package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"

	"github.com/brf-tech/filex-apps/backend/internal/db"
	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/store"
)

// Allowed image types. No SVG: it can carry script.
const (
	PNG  = "image/png"
	JPEG = "image/jpeg"
	WebP = "image/webp"
)

// MaxDimension bounds both sides of an image (a header claiming more is
// refused before anything is decoded).
const MaxDimension = 8192

// ErrInvalid is wrapped by every refusal.
var ErrInvalid = errors.New("media invalid")

// Sink stores images; it is the pipeline's MediaSink.
type Sink struct {
	DB       db.Querier
	MaxBytes int64
}

// Put checks body against m (sha256, type, dimensions, size) and stores it.
// The same sha256 twice is one row.
func (s *Sink) Put(ctx context.Context, m domain.MediaRef, sourceURL string, body []byte) error {
	if s.MaxBytes > 0 && int64(len(body)) > s.MaxBytes {
		return fmt.Errorf("%w: %d bytes, the limit is %d", ErrInvalid, len(body), s.MaxBytes)
	}
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != m.SHA256 {
		return fmt.Errorf("%w: sha256 %s, expected %s", ErrInvalid, got, m.SHA256)
	}
	typ, w, h, err := Inspect(body)
	if err != nil {
		return err
	}
	if m.Type != "" && m.Type != typ {
		return fmt.Errorf("%w: the bytes are %s, declared %s", ErrInvalid, typ, m.Type)
	}
	if (m.Width != 0 && m.Width != w) || (m.Height != 0 && m.Height != h) {
		return fmt.Errorf("%w: %dx%d, declared %dx%d", ErrInvalid, w, h, m.Width, m.Height)
	}
	return store.PutMedia(ctx, s.DB, store.Media{
		SHA256: m.SHA256, ContentType: typ, Width: w, Height: h, SourceURL: sourceURL, Blob: body,
	})
}

// Inspect sniffs an image's type from its bytes and reads its dimensions
// from the header (nothing is decoded beyond it).
func Inspect(b []byte) (contentType string, width, height int, err error) {
	var cfg image.Config
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		contentType = PNG
		cfg, err = png.DecodeConfig(bytes.NewReader(b))
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		contentType = JPEG
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(b))
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		contentType = WebP
		cfg.Width, cfg.Height, err = webpSize(b)
	default:
		return "", 0, 0, fmt.Errorf("%w: not a PNG, JPEG or WebP image", ErrInvalid)
	}
	if err != nil {
		return "", 0, 0, fmt.Errorf("%w: %s header: %v", ErrInvalid, contentType, err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxDimension || cfg.Height > MaxDimension {
		return "", 0, 0, fmt.Errorf("%w: %dx%d is out of bounds", ErrInvalid, cfg.Width, cfg.Height)
	}
	return contentType, cfg.Width, cfg.Height, nil
}

// webpSize reads the canvas size of a WebP file (VP8, VP8L or VP8X first
// chunk).
func webpSize(b []byte) (int, int, error) {
	if len(b) < 30 {
		return 0, 0, errors.New("too short")
	}
	riffSize := binary.LittleEndian.Uint32(b[4:8])
	if int64(riffSize)+8 > int64(len(b)) {
		return 0, 0, errors.New("truncated")
	}
	chunk := string(b[12:16])
	data := b[20:]
	switch chunk {
	case "VP8 ":
		// frame tag (3 bytes), start code 9d 01 2a, then 14-bit width/height.
		if len(data) < 10 || data[3] != 0x9d || data[4] != 0x01 || data[5] != 0x2a {
			return 0, 0, errors.New("bad VP8 frame header")
		}
		w := int(binary.LittleEndian.Uint16(data[6:8]) & 0x3fff)
		h := int(binary.LittleEndian.Uint16(data[8:10]) & 0x3fff)
		return w, h, nil
	case "VP8L":
		if len(data) < 5 || data[0] != 0x2f {
			return 0, 0, errors.New("bad VP8L signature")
		}
		bits := binary.LittleEndian.Uint32(data[1:5])
		w := int(bits&0x3fff) + 1
		h := int((bits>>14)&0x3fff) + 1
		return w, h, nil
	case "VP8X":
		if len(data) < 10 {
			return 0, 0, errors.New("bad VP8X header")
		}
		w := int(uint32(data[4])|uint32(data[5])<<8|uint32(data[6])<<16) + 1
		h := int(uint32(data[7])|uint32(data[8])<<8|uint32(data[9])<<16) + 1
		return w, h, nil
	}
	return 0, 0, fmt.Errorf("unknown first chunk %q", chunk)
}
