package index

import (
	"bytes"
	"encoding/json"
)

// Canonical writes v as the index's canonical bytes: object keys sorted,
// two-space indent, no HTML escaping, UTF-8, a trailing newline. The same
// value always gives the same bytes.
func Canonical(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // numbers keep their exact text
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(tree); err != nil { // Encode ends with "\n"
		return nil, err
	}
	return buf.Bytes(), nil
}

// sameContent reports whether body (a stored release) describes the same
// catalog as doc, ignoring what changes at every release (serial and the two
// times).
func sameContent(body []byte, doc Document) bool {
	var old Document
	if err := json.Unmarshal(body, &old); err != nil {
		return false
	}
	old.Serial, old.GeneratedAt, old.ExpiresAt = 0, "", ""
	doc.Serial, doc.GeneratedAt, doc.ExpiresAt = 0, "", ""
	a, err1 := Canonical(old)
	b, err2 := Canonical(doc)
	return err1 == nil && err2 == nil && bytes.Equal(a, b)
}
