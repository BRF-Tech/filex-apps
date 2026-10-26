package main

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The avatar is only ever read from GitHub's avatar host, at 256 px.
func TestAvatarURL_GitHubsAvatarHostOnly(t *testing.T) {
	u, err := avatarURL("https://avatars.githubusercontent.com/u/159972728?v=4")
	if err != nil || u != "https://avatars.githubusercontent.com/u/159972728?s=256&v=4" {
		t.Errorf("%q %v", u, err)
	}
	for _, bad := range []string{
		"http://avatars.githubusercontent.com/u/1",
		"https://evil.example/u/1",
		"https://user@avatars.githubusercontent.com/u/1",
		"https://avatars.githubusercontent.com.evil.example/u/1",
		"not a url",
	} {
		if _, err := avatarURL(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// An avatar is a square PNG, JPEG or WebP within bounds that decodes.
func TestCheckAvatar(t *testing.T) {
	ref, err := checkAvatar(pngBytes(t, 256, 256))
	if err != nil || ref.Type != "image/png" || ref.Width != 256 || ref.Role != "avatar" {
		t.Errorf("a 256 px PNG: %+v %v", ref, err)
	}
	var j bytes.Buffer
	_ = jpeg.Encode(&j, image.NewRGBA(image.Rect(0, 0, 64, 64)), nil)
	if ref, err := checkAvatar(j.Bytes()); err != nil || ref.Type != "image/jpeg" {
		t.Errorf("a JPEG: %+v %v", ref, err)
	}
	truncated := pngBytes(t, 256, 256)
	truncated = truncated[:len(truncated)/2]
	for name, b := range map[string][]byte{
		"not square": pngBytes(t, 256, 200),
		"too small":  pngBytes(t, 16, 16),
		"too large":  pngBytes(t, 1100, 1100),
		"svg":        []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`),
		"truncated":  truncated,
	} {
		if _, err := checkAvatar(b); err == nil {
			t.Errorf("%s was accepted", name)
		} else if strings.TrimSpace(err.Error()) == "" {
			t.Errorf("%s: an empty reason", name)
		}
	}
}
