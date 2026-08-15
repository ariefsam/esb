package naming

import "testing"

func TestToPascalCase(t *testing.T) {
	cases := map[string]string{
		"bank_account": "BankAccount",
		"buyer_id":     "BuyerID",
		"http_url":     "HTTPURL",
		"order":        "Order",
		"user_uuid":    "UserUUID",
	}
	for in, want := range cases {
		if got := ToPascalCase(in); got != want {
			t.Errorf("ToPascalCase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToSnakeCase(t *testing.T) {
	cases := map[string]string{
		"BankAccount": "bank_account",
		"BuyerID":     "buyer_id",
		"HTTPRequest": "http_request",
		"Order":       "order",
	}
	for in, want := range cases {
		if got := ToSnakeCase(in); got != want {
			t.Errorf("ToSnakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToKebabCase(t *testing.T) {
	if got := ToKebabCase("bank_account"); got != "bank-account" {
		t.Errorf("ToKebabCase() = %q, want %q", got, "bank-account")
	}
}

func TestToPlural(t *testing.T) {
	cases := map[string]string{
		// consonant + y → ies
		"policy":   "policies",
		"category": "categories",
		"entity":   "entities",
		// vowel + y → ys (regression for C3: was "gatewaies")
		"gateway": "gateways",
		"survey":  "surveys",
		"journey": "journeys",
		"day":     "days",
		"key":     "keys",
		// plain nouns
		"user":         "users",
		"order":        "orders",
		"bank_account": "bank_accounts",
		// degenerate: bare "y" has no letter before it → treat as vowel-ish, just +s
		"y": "ys",
	}
	for in, want := range cases {
		if got := ToPlural(in); got != want {
			t.Errorf("ToPlural(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPackageName(t *testing.T) {
	cases := map[string]string{
		"github.com/myorg/toko-online": "tokoonline",
		"pasaremas":                    "pasaremas",
		"example.com/Shop_App":         "shopapp",
	}
	for in, want := range cases {
		if got := PackageName(in); got != want {
			t.Errorf("PackageName(%q) = %q, want %q", in, got, want)
		}
	}
}
