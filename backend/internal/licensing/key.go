package licensing

import (
	"crypto/rand"
	"strings"
)

// The license key: "FXL-" and 26 Crockford base32 characters (130 random
// bits) in groups of four, the last group two:
//
//	FXL-7K2Q-9WXM-3HDF-0PZA-QRST-V8MN-4C
//
// Crockford's alphabet leaves out I, L, O and U; reading a key back forgives
// case, spaces, missing dashes and the look-alikes (I and L read as 1, O as
// 0), so a key typed from paper still matches.
const (
	keyPrefix   = "FXL-"
	keyChars    = 26
	alphabet    = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	PrefixChars = 8 // licenses.key_prefix: the key's first 8 characters
)

// NewKey makes a random key in its canonical form. Each character takes the
// low five bits of one random byte (256 is a multiple of 32: uniform).
func NewKey() string {
	b := make([]byte, keyChars)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	raw := make([]byte, keyChars)
	for i, x := range b {
		raw[i] = alphabet[x&31]
	}
	return group(string(raw))
}

func group(raw string) string {
	var sb strings.Builder
	sb.WriteString(keyPrefix)
	for i := 0; i < len(raw); i += 4 {
		if i > 0 {
			sb.WriteByte('-')
		}
		end := min(i+4, len(raw))
		sb.WriteString(raw[i:end])
	}
	return sb.String()
}

// NormalizeKey reads a key as a person typed it and returns its canonical
// form, or false when it is not a well-formed key.
func NormalizeKey(s string) (string, bool) {
	if len(s) > 64 {
		return "", false
	}
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "FXL")
	var raw []byte
	for _, r := range s {
		switch {
		case r == '-' || r == ' ':
			continue
		case r == 'I' || r == 'L':
			r = '1'
		case r == 'O':
			r = '0'
		}
		if r > 0x7f || !strings.ContainsRune(alphabet, r) {
			return "", false
		}
		raw = append(raw, byte(r))
	}
	if len(raw) != keyChars {
		return "", false
	}
	return group(string(raw)), true
}

// Prefix is what licenses.key_prefix keeps of a canonical key.
func Prefix(canonical string) string { return canonical[:PrefixChars] }
