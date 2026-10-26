package audit

import (
	"encoding/json"
	"testing"
	"time"
)

func chain(t *testing.T, n int) []Row {
	t.Helper()
	var rows []Row
	prev := ""
	at := time.Date(2026, 10, 3, 12, 0, 0, 123456000, time.UTC)
	for i := 1; i <= n; i++ {
		acct := int64(i)
		r := Row{ID: int64(i), At: at.Add(time.Duration(i) * time.Second), ActorAccountID: &acct, ActorRole: RoleAdmin,
			Action: "app.revoke", TargetType: "app", TargetID: "sign", IP: "203.0.113.7",
			Data: json.RawMessage(`{"reason":"bad <script>", "n": 3}`), PrevHash: prev}
		h, err := Hash(prev, r)
		if err != nil {
			t.Fatal(err)
		}
		r.Hash = h
		prev = h
		rows = append(rows, r)
	}
	return rows
}

func TestCanonicalSortsKeysAndKeepsHTMLAndNumbers(t *testing.T) {
	got, err := Canonical([]byte(`{ "b": 1, "a": {"z": "<&>", "y": 12345678901234567890} }`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":{"y":12345678901234567890,"z":"<&>"},"b":1}`
	if string(got) != want {
		t.Fatalf("canonical = %s, want %s", got, want)
	}
	if empty, _ := Canonical(nil); string(empty) != "{}" {
		t.Fatalf("empty data = %s, want {}", empty)
	}
}

func TestHashIsDeterministicAndCoversEveryField(t *testing.T) {
	rows := chain(t, 1)
	base := rows[0]
	h1, _ := Hash("", base)
	h2, _ := Hash("", base)
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("hash not deterministic: %s %s", h1, h2)
	}
	// The same data written differently (jsonb reorders keys and adds
	// spaces) hashes the same.
	same := base
	same.Data = json.RawMessage(`{"n":3,  "reason":"bad <script>"}`)
	if h, _ := Hash("", same); h != h1 {
		t.Fatal("key order or spacing changed the hash")
	}
	other := int64(99)
	for name, mut := range map[string]func(r *Row){
		"prev":   func(r *Row) {},
		"at":     func(r *Row) { r.At = r.At.Add(time.Microsecond) },
		"actor":  func(r *Row) { r.ActorAccountID = &other },
		"role":   func(r *Row) { r.ActorRole = RoleReviewer },
		"action": func(r *Row) { r.Action = "app.unrevoke" },
		"target": func(r *Row) { r.TargetID = "convert" },
		"ip":     func(r *Row) { r.IP = "203.0.113.8" },
		"data":   func(r *Row) { r.Data = json.RawMessage(`{"reason":"fine"}`) },
	} {
		r := base
		mut(&r)
		prev := ""
		if name == "prev" {
			prev = "00"
		}
		if h, _ := Hash(prev, r); h == h1 {
			t.Errorf("changing %s did not change the hash", name)
		}
	}
}

func TestVerifyChainFindsTheFirstBrokenRow(t *testing.T) {
	rows := chain(t, 5)
	if broken, last := VerifyChain(rows, ""); broken != 0 || last != rows[4].Hash {
		t.Fatalf("intact chain: broken=%d", broken)
	}

	edited := append([]Row{}, rows...)
	edited[2].Data = json.RawMessage(`{"reason":"rewritten"}`)
	if broken, _ := VerifyChain(edited, ""); broken != 3 {
		t.Fatalf("rewritten row 3: broken = %d", broken)
	}

	removed := append(append([]Row{}, rows[:1]...), rows[2:]...)
	if broken, _ := VerifyChain(removed, ""); broken != 3 {
		t.Fatalf("removed row 2: broken = %d, want 3 (its prev_hash no longer follows)", broken)
	}

	swapped := append([]Row{}, rows...)
	swapped[1], swapped[2] = swapped[2], swapped[1]
	if broken, _ := VerifyChain(swapped, ""); broken == 0 {
		t.Fatal("reordered rows verified")
	}
}

func TestNormalizeIP(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7":            "203.0.113.7",
		"::ffff:203.0.113.7":     "203.0.113.7",
		"2001:0db8:0000::0001":   "2001:db8::1",
		"":                       "",
		"not an address":         "",
		"203.0.113.7:443":        "",
		"2001:DB8:0:0:0:0:0:abc": "2001:db8::abc",
	} {
		if got := NormalizeIP(in); got != want {
			t.Errorf("NormalizeIP(%q) = %q, want %q", in, got, want)
		}
	}
}
