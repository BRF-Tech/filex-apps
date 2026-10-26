package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// webp builds a RIFF container with one chunk.
func webp(chunk string, data []byte) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(4+8+len(data)))
	b.WriteString("WEBP")
	b.WriteString(chunk)
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	for b.Len() < 40 {
		b.WriteByte(0)
	}
	return b.Bytes()
}

func TestInspect(t *testing.T) {
	vp8 := []byte{0x10, 0x02, 0x00, 0x9d, 0x01, 0x2a, 0, 0, 0, 0, 0, 0}
	binary.LittleEndian.PutUint16(vp8[6:], 640)
	binary.LittleEndian.PutUint16(vp8[8:], 480)
	vp8l := []byte{0x2f, 0, 0, 0, 0, 0}
	binary.LittleEndian.PutUint32(vp8l[1:], uint32(255)|uint32(127)<<14)
	vp8x := []byte{0, 0, 0, 0, 0xff, 0x03, 0x00, 0xff, 0x01, 0x00, 0, 0}

	for name, c := range map[string]struct {
		b    []byte
		typ  string
		w, h int
	}{
		"png":  {pngBytes(t, 128, 64), PNG, 128, 64},
		"jpeg": {jpegBytes(t, 33, 17), JPEG, 33, 17},
		"vp8":  {webp("VP8 ", vp8), WebP, 640, 480},
		"vp8l": {webp("VP8L", vp8l), WebP, 256, 128},
		"vp8x": {webp("VP8X", vp8x), WebP, 1024, 512},
	} {
		typ, w, h, err := Inspect(c.b)
		if err != nil || typ != c.typ || w != c.w || h != c.h {
			t.Errorf("%s: Inspect = %s %dx%d %v, want %s %dx%d", name, typ, w, h, err, c.typ, c.w, c.h)
		}
	}
}

func TestInspectRefuses(t *testing.T) {
	huge := webp("VP8X", []byte{0, 0, 0, 0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})
	for name, b := range map[string][]byte{
		"svg":       []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"></svg>`),
		"gif":       []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;"),
		"empty":     nil,
		"broken":    []byte("\x89PNG\r\n\x1a\nnot really"),
		"truncated": []byte("RIFF\xff\xff\x00\x00WEBPVP8 \x00\x00\x00\x00"),
		"too large": huge,
	} {
		if _, _, _, err := Inspect(b); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

// fakeDB records Exec calls.
type fakeDB struct {
	execs [][]any
}

func (f *fakeDB) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	f.execs = append(f.execs, args)
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}
func (f *fakeDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("no")
}
func (f *fakeDB) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func shaOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestSinkStoresOnlyWhatItWasPromised(t *testing.T) {
	body := pngBytes(t, 256, 256)
	ref := domain.MediaRef{Role: "icon", SHA256: shaOf(body), Type: PNG, Width: 256, Height: 256}

	db := &fakeDB{}
	s := &Sink{DB: db, MaxBytes: 1 << 20}
	if err := s.Put(context.Background(), ref, "https://raw.example/icon.png", body); err != nil {
		t.Fatal(err)
	}
	if len(db.execs) != 1 || db.execs[0][0] != ref.SHA256 || db.execs[0][1] != PNG {
		t.Fatalf("stored %v", db.execs)
	}

	for name, c := range map[string]struct {
		ref  domain.MediaRef
		body []byte
		max  int64
	}{
		"sha256":    {domain.MediaRef{SHA256: shaOf([]byte("other")), Type: PNG}, body, 0},
		"type":      {domain.MediaRef{SHA256: ref.SHA256, Type: JPEG}, body, 0},
		"dimension": {domain.MediaRef{SHA256: ref.SHA256, Type: PNG, Width: 128, Height: 128}, body, 0},
		"size":      {ref, body, 10},
		"not image": {domain.MediaRef{SHA256: shaOf([]byte("<svg/>")), Type: PNG}, []byte("<svg/>"), 0},
	} {
		db := &fakeDB{}
		err := (&Sink{DB: db, MaxBytes: c.max}).Put(context.Background(), c.ref, "u", c.body)
		if !errors.Is(err, ErrInvalid) || len(db.execs) != 0 {
			t.Errorf("%s: err = %v, stored %d rows; want ErrInvalid and nothing stored", name, err, len(db.execs))
		}
	}
}
