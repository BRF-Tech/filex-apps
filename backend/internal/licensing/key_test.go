package licensing

import (
	"regexp"
	"strings"
	"testing"

	"github.com/brf-tech/filex-apps/backend/internal/domain"
)

// The contract's pattern (api/openapi.yaml LicenseKey).
var contractKey = regexp.MustCompile(`^FXL-[0-9A-HJKMNP-TV-Z]{4}(-[0-9A-HJKMNP-TV-Z]{4}){5}-[0-9A-HJKMNP-TV-Z]{2}$`)

func TestNewKeyHasTheContractsShapeAndIsRandom(t *testing.T) {
	seen := map[string]bool{}
	counts := map[byte]int{}
	for i := 0; i < 2000; i++ {
		k := NewKey()
		if !contractKey.MatchString(k) || len(k) != 36 {
			t.Fatalf("key %q does not have the contract's shape", k)
		}
		if seen[k] {
			t.Fatalf("key %q came twice", k)
		}
		seen[k] = true
		for _, c := range []byte(strings.ReplaceAll(strings.TrimPrefix(k, "FXL-"), "-", "")) {
			counts[c]++
		}
		if c, ok := NormalizeKey(k); !ok || c != k {
			t.Fatalf("a new key does not read back: %q -> %q %v", k, c, ok)
		}
	}
	if len(counts) != 32 {
		t.Fatalf("only %d of the 32 characters were drawn", len(counts))
	}
	for _, c := range "ILOU" {
		if counts[byte(c)] != 0 {
			t.Fatalf("%c is not in Crockford's alphabet", c)
		}
	}
}

func TestNormalizeKeyForgivesWhatPeopleType(t *testing.T) {
	want := "FXL-7K2Q-9WXM-3HDF-0PZA-QRST-V8MN-41"
	for _, in := range []string{
		want, " fxl-7k2q-9wxm-3hdf-0pza-qrst-v8mn-41 ", "7K2Q9WXM3HDF0PZAQRSTV8MN41", "FXL 7K2Q 9WXM 3HDF 0PZA QRST V8MN 41",
		"FXL-7K2Q-9WXM-3HDF-OPZA-QRST-V8MN-4I", "FXL-7K2Q-9WXM-3HDF-oPZA-QRST-V8MN-4l",
	} {
		if got, ok := NormalizeKey(in); !ok || got != want {
			t.Errorf("%q -> %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "FXL-", "FXL-7K2Q-9WXM-3HDF-0PZA-QRST-V8MN-4", "FXL-7K2Q-9WXM-3HDF-0PZA-QRST-V8MN-412",
		"FXL-7K2Q-9WXM-3HDF-0PZA-QRST-V8MN-4U", "FXL-7K2Q-9WXM-3HDF-0PZA-QRST-V8MN-4Ş", strings.Repeat("A", 80)} {
		if got, ok := NormalizeKey(in); ok {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
	if Prefix(want) != "FXL-7K2Q" {
		t.Fatalf("prefix = %q", Prefix(want))
	}
}

func TestCheckPricing(t *testing.T) {
	for _, ok := range []domain.Pricing{
		{Model: domain.PricingFree},
		{Model: domain.PricingPaid},
		{Model: domain.PricingPaid, PriceText: "$29 per instance", PurchaseURL: "https://shop.example.com/buy/filex-sign?plan=1"},
	} {
		if _, err := CheckPricing(ok); err != nil {
			t.Errorf("%+v refused: %v", ok, err)
		}
	}
	for _, bad := range []domain.Pricing{
		{Model: "gratis"},
		{Model: domain.PricingFree, PriceText: "$0"},
		{Model: domain.PricingFree, PurchaseURL: "https://x.example"},
		{Model: domain.PricingPaid, PurchaseURL: "http://x.example/buy"},
		{Model: domain.PricingPaid, PurchaseURL: "javascript:alert(1)"},
		{Model: domain.PricingPaid, PurchaseURL: "https://user:pw@x.example"},
		{Model: domain.PricingPaid, PriceText: strings.Repeat("x", 81)},
		{Model: domain.PricingPaid, PriceText: "a b"},
	} {
		if _, err := CheckPricing(bad); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
