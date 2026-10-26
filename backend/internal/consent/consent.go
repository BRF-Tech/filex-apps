// Package consent reads a repository's `.filex-apps.json` - the owner's
// consent to be listed under a publisher, and the listing's metadata - and
// holds it to schema/filex-apps.schema.json.
//
// The validation is hand-written (no JSON Schema dependency) and mirrors the
// schema rule by rule; consent_test.go reads the schema file and fails when
// a limit or pattern here drifts from it.
package consent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
	"github.com/brf-tech/filex-apps/backend/internal/filetypes"
)

// FileName is where the file lives: the repository root, next to
// filex-app.json.
const FileName = ".filex-apps.json"

// MaxBytes caps the file (the schema's longest document is far below it).
const MaxBytes = 64 << 10

// The schema's limits (schema/filex-apps.schema.json).
const (
	MaxCategories  = 3
	MaxScreenshots = 8
	MaxPathLen     = 300
	MaxTextLen     = 300
	// conversions: entries, extensions in one entry's from or to, engines in
	// one entry's needs, and from x to summed over every entry.
	MaxConversions     = 256
	MaxConversionExts  = 128
	MaxConversionNeeds = 6
	MaxConversionPairs = 16384
)

// The schema's patterns. The path rule has a look-ahead in the schema
// (no leading "/", no ".." segment) that Go's regexp lacks: pathChars is the
// character class and validPath adds the two look-aheads.
var (
	publisherRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$`)
	categoryRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	langRe      = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
	pathChars   = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
	// extRe is filex's grammar of an extension (filetypes.ExtPattern), as
	// conversions name kinds: lower-case, no leading dot, up to three
	// dotted parts (tar.gz).
	extRe = regexp.MustCompile(filetypes.ExtPattern)
	// engineRe is an engine id as a manifest's `engines:<id>` permission
	// names it.
	engineRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
)

// Error is a consent file that does not satisfy the schema; Problems says
// every way it does not, each naming its JSON path.
type Error struct {
	Problems []string
}

func (e *Error) Error() string {
	return ".filex-apps.json: " + strings.Join(e.Problems, "; ")
}

// Parse reads and validates a consent file. A *Error lists every problem; a
// valid file comes back as domain.Consent.
func Parse(b []byte) (*domain.Consent, error) {
	if len(b) > MaxBytes {
		return nil, &Error{Problems: []string{fmt.Sprintf("the file is larger than %d bytes", MaxBytes)}}
	}
	if !utf8.Valid(b) {
		return nil, &Error{Problems: []string{"the file is not UTF-8"}}
	}
	var top map[string]json.RawMessage
	if err := strictUnmarshal(b, &top); err != nil {
		return nil, &Error{Problems: []string{"not a JSON object: " + err.Error()}}
	}
	v := &validator{}
	out := &domain.Consent{}
	keys := make([]string, 0, len(top))
	for k := range top {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		raw := top[k]
		switch k {
		case "publisher":
			if s, ok := v.str("publisher", raw); ok {
				if !publisherRe.MatchString(s) {
					v.add("publisher: %q must match %s", s, publisherRe)
				}
				out.Publisher = s
			}
		case "categories":
			out.Categories = v.categories(raw)
		case "summary":
			out.Summary = v.text("summary", raw)
			if out.Summary != nil && out.Summary["en"] == "" {
				v.add("summary: \"en\" is required")
			}
		case "description":
			if s, ok := v.path("description", raw); ok {
				out.Description = s
			}
		case "icon":
			if s, ok := v.path("icon", raw); ok {
				out.Icon = s
			}
		case "screenshots":
			out.Screenshots = v.screenshots(raw)
		case "conversions":
			out.Conversions = v.conversions(raw)
		default:
			v.add("%s: unknown field", k)
		}
	}
	if _, ok := top["publisher"]; !ok {
		v.add("publisher: required")
	}
	if len(v.problems) > 0 {
		return nil, &Error{Problems: v.problems}
	}
	return out, nil
}

// strictUnmarshal decodes one JSON value and refuses trailing data and
// duplicate keys at the top level (a second "publisher" would otherwise
// silently win).
func strictUnmarshal(b []byte, top *map[string]json.RawMessage) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("the document must be an object")
	}
	m := map[string]json.RawMessage{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return err
		}
		k, _ := kt.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		if _, dup := m[k]; dup {
			return fmt.Errorf("%q appears twice", k)
		}
		m[k] = raw
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("data after the object")
	}
	*top = m
	return nil
}

type validator struct {
	problems []string
}

func (v *validator) add(f string, a ...any) { v.problems = append(v.problems, fmt.Sprintf(f, a...)) }

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func (v *validator) str(at string, raw json.RawMessage) (string, bool) {
	var s string
	if isNull(raw) || json.Unmarshal(raw, &s) != nil {
		v.add("%s: must be a string", at)
		return "", false
	}
	return s, true
}

// validPath is the schema's $defs/path: 1-300 characters of
// [A-Za-z0-9._/-], not starting with "/", no ".." segment.
func validPath(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > MaxPathLen || !pathChars.MatchString(s) || strings.HasPrefix(s, "/") {
		return false
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}

func (v *validator) path(at string, raw json.RawMessage) (string, bool) {
	s, ok := v.str(at, raw)
	if !ok {
		return "", false
	}
	if !validPath(s) {
		v.add("%s: %q is not a path in the repository (letters, digits, . _ - /; not absolute; no ..; at most %d characters)", at, s, MaxPathLen)
		return "", false
	}
	return s, true
}

func (v *validator) text(at string, raw json.RawMessage) domain.Text {
	var m map[string]json.RawMessage
	if isNull(raw) || json.Unmarshal(raw, &m) != nil || m == nil {
		v.add("%s: must be an object of {language: text}", at)
		return nil
	}
	out := domain.Text{}
	for lang, val := range m {
		if !langRe.MatchString(lang) {
			v.add("%s: %q is not a language tag", at, lang)
			continue
		}
		s, ok := v.str(at+"."+lang, val)
		if !ok {
			continue
		}
		n := utf8.RuneCountInString(s)
		if n < 1 || n > MaxTextLen {
			v.add("%s.%s: 1 to %d characters", at, lang, MaxTextLen)
			continue
		}
		out[lang] = s
	}
	return out
}

func (v *validator) categories(raw json.RawMessage) []string {
	var items []json.RawMessage
	if isNull(raw) || json.Unmarshal(raw, &items) != nil {
		v.add("categories: must be an array")
		return nil
	}
	if len(items) > MaxCategories {
		v.add("categories: at most %d", MaxCategories)
	}
	seen := map[string]bool{}
	var out []string
	for i, it := range items {
		s, ok := v.str(fmt.Sprintf("categories[%d]", i), it)
		if !ok {
			continue
		}
		if !categoryRe.MatchString(s) {
			v.add("categories[%d]: %q must match %s", i, s, categoryRe)
			continue
		}
		if seen[s] {
			v.add("categories: %q appears twice", s)
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func (v *validator) screenshots(raw json.RawMessage) []domain.ConsentShot {
	var items []json.RawMessage
	if isNull(raw) || json.Unmarshal(raw, &items) != nil {
		v.add("screenshots: must be an array")
		return nil
	}
	if len(items) > MaxScreenshots {
		v.add("screenshots: at most %d", MaxScreenshots)
	}
	var out []domain.ConsentShot
	for i, it := range items {
		at := fmt.Sprintf("screenshots[%d]", i)
		var m map[string]json.RawMessage
		if isNull(it) || json.Unmarshal(it, &m) != nil || m == nil {
			v.add("%s: must be an object", at)
			continue
		}
		shot := domain.ConsentShot{}
		okPath := false
		for k, val := range m {
			switch k {
			case "path":
				shot.Path, okPath = v.path(at+".path", val)
			case "caption":
				shot.Caption = v.text(at+".caption", val)
			default:
				v.add("%s.%s: unknown field", at, k)
			}
		}
		if _, has := m["path"]; !has {
			v.add("%s.path: required", at)
		}
		if okPath {
			out = append(out, shot)
		}
	}
	return out
}

// conversions reads `conversions`: 1 to MaxConversions entries, each with
// `from` and `to` (1 to MaxConversionExts extensions in filex's grammar,
// each once, no extension in both) and an optional `needs` (1 to
// MaxConversionNeeds engine ids, each once); at most MaxConversionPairs
// from-to pairs in all. Whether `needs` names engines the manifest asks for
// is CheckNeeds' question (it needs the manifest).
func (v *validator) conversions(raw json.RawMessage) []filetypes.Conversion {
	var items []json.RawMessage
	if isNull(raw) || json.Unmarshal(raw, &items) != nil {
		v.add("conversions: must be an array")
		return nil
	}
	if len(items) == 0 {
		v.add("conversions: at least 1 entry; leave the field out when the app converts nothing")
		return nil
	}
	if len(items) > MaxConversions {
		v.add("conversions: at most %d entries", MaxConversions)
	}
	var out []filetypes.Conversion
	pairs := 0
	for i, it := range items {
		at := fmt.Sprintf("conversions[%d]", i)
		var m map[string]json.RawMessage
		if isNull(it) || json.Unmarshal(it, &m) != nil || m == nil {
			v.add("%s: must be an object", at)
			continue
		}
		c := filetypes.Conversion{}
		ok := true
		for _, k := range []string{"from", "to"} {
			if _, has := m[k]; !has {
				v.add("%s.%s: required", at, k)
				ok = false
			}
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			val := m[k]
			switch k {
			case "from":
				c.From = v.extList(at+".from", val)
				ok = ok && c.From != nil
			case "to":
				c.To = v.extList(at+".to", val)
				ok = ok && c.To != nil
			case "needs":
				c.Needs = v.engines(at+".needs", val)
				ok = ok && c.Needs != nil
			default:
				v.add("%s.%s: unknown field", at, k)
				ok = false
			}
		}
		if !ok {
			continue
		}
		for _, e := range c.From {
			for _, t := range c.To {
				if e == t {
					v.add("%s: %q is in both from and to (a kind converted to itself is not a conversion)", at, e)
					ok = false
				}
			}
		}
		pairs += len(c.From) * len(c.To)
		if ok {
			out = append(out, c)
		}
	}
	if pairs > MaxConversionPairs {
		v.add("conversions: %d from-to pairs, at most %d", pairs, MaxConversionPairs)
	}
	return out
}

// extList is a conversion's `from` or `to`: 1 to MaxConversionExts
// extensions, each once; nil when it is not one.
func (v *validator) extList(at string, raw json.RawMessage) []string {
	return v.idList(at, raw, MaxConversionExts, extRe, "an extension (lower-case, no leading dot, like docx or tar.gz)")
}

// engines is a conversion's `needs`: 1 to MaxConversionNeeds engine ids,
// each once; nil when it is not one.
func (v *validator) engines(at string, raw json.RawMessage) []string {
	return v.idList(at, raw, MaxConversionNeeds, engineRe, "an engine id (as in an engines:<id> permission)")
}

func (v *validator) idList(at string, raw json.RawMessage, max int, re *regexp.Regexp, what string) []string {
	var items []json.RawMessage
	if isNull(raw) || json.Unmarshal(raw, &items) != nil {
		v.add("%s: must be an array", at)
		return nil
	}
	if len(items) == 0 || len(items) > max {
		v.add("%s: 1 to %d items", at, max)
		return nil
	}
	out := []string{}
	seen := map[string]bool{}
	ok := true
	for i, it := range items {
		s, isStr := v.str(fmt.Sprintf("%s[%d]", at, i), it)
		if !isStr {
			ok = false
			continue
		}
		if !re.MatchString(s) {
			v.add("%s[%d]: %q is not %s", at, i, s, what)
			ok = false
			continue
		}
		if seen[s] {
			v.add("%s: %q appears twice", at, s)
			ok = false
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	if !ok {
		return nil
	}
	return out
}

// EnginePermission is the manifest permission that lets an app use an
// engine: `engines:<id>`.
const EnginePermission = "engines:"

// CheckNeeds says, for each engine a conversion needs that the version may
// not use, why: an engine a conversion needs must be one the manifest asks
// for (an `engines:<id>` permission, or its `engines` list), or the listing
// promises what the app cannot do. Empty when every one is.
func CheckNeeds(convs []filetypes.Conversion, permissions, manifestEngines []string) []string {
	may := map[string]bool{}
	for _, p := range permissions {
		if strings.HasPrefix(p, EnginePermission) {
			may[strings.TrimPrefix(p, EnginePermission)] = true
		}
	}
	for _, e := range manifestEngines {
		may[e] = true
	}
	var problems []string
	for i, c := range convs {
		for j, e := range c.Needs {
			if !may[e] {
				problems = append(problems, fmt.Sprintf("conversions[%d].needs[%d]: %q is not an engine the manifest asks for (no %s%s permission)", i, j, e, EnginePermission, e))
			}
		}
	}
	return problems
}

// Pairs is how many from-to pairs the conversions declare, each condition
// counted apart.
func Pairs(convs []filetypes.Conversion) int {
	n := 0
	for _, c := range convs {
		n += len(c.From) * len(c.To)
	}
	return n
}
