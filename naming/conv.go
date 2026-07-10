package naming

import (
	"strings"
	"unicode"
)

// Go initialisms that should remain ALL_CAPS in identifiers.
var initialisms = map[string]bool{
	"ACL": true, "API": true, "ASCII": true, "CPU": true, "CSS": true,
	"DNS": true, "EOF": true, "GUID": true, "HTML": true, "HTTP": true,
	"HTTPS": true, "ID": true, "IP": true, "JSON": true, "LHS": true,
	"QPS": true, "RAM": true, "RHS": true, "RPC": true, "SLA": true,
	"SMTP": true, "SQL": true, "SSH": true, "TCP": true, "TLS": true,
	"TTL": true, "UDP": true, "UI": true, "UID": true, "UUID": true,
	"URI": true, "URL": true, "UTF8": true, "VM": true, "XML": true,
	"XMPP": true, "XSRF": true, "XSS": true,
}

// ToPascalCase converts snake_case to PascalCase, respecting Go initialisms.
//
//	"bank_account" → "BankAccount"
//	"buyer_id"     → "BuyerID"
//	"http_url"     → "HTTPURL"
func ToPascalCase(s string) string {
	parts := strings.Split(s, "_")
	var b strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		upper := strings.ToUpper(part)
		if initialisms[upper] {
			b.WriteString(upper)
		} else {
			b.WriteRune(unicode.ToUpper(rune(part[0])))
			b.WriteString(part[1:])
		}
	}
	return b.String()
}

// ToSnakeCase converts PascalCase to snake_case, handling Go initialisms.
//
//	"BankAccount" → "bank_account"
//	"BuyerID"     → "buyer_id"
//	"HTTPRequest" → "http_request"
func ToSnakeCase(s string) string {
	runes := []rune(s)
	var result []rune
	for i, r := range runes {
		if unicode.IsUpper(r) {
			if i > 0 {
				prev := runes[i-1]
				if unicode.IsLower(prev) || unicode.IsDigit(prev) {
					result = append(result, '_')
				} else if i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
					result = append(result, '_')
				}
			}
			result = append(result, unicode.ToLower(r))
		} else {
			result = append(result, r)
		}
	}
	return string(result)
}

// ToKebabCase converts snake_case to kebab-case.
//
//	"bank_account" → "bank-account"
func ToKebabCase(s string) string {
	return strings.ReplaceAll(s, "_", "-")
}

// ToPlural returns a naive plural form suitable for table names.
//
//	"user"         → "users"
//	"policy"       → "policies"
//	"bank_account" → "bank_accounts"
func ToPlural(s string) string {
	if strings.HasSuffix(s, "y") {
		// consonant+y → ies; vowel+y → ys (good enough for table names)
		return s[:len(s)-1] + "ies"
	}
	return s + "s"
}

// PackageName returns a valid Go package name from a module path.
//
//	"github.com/myorg/toko-online" → "tokoonline"
//	"pasaremas"                    → "pasaremas"
func PackageName(moduleName string) string {
	parts := strings.Split(moduleName, "/")
	last := parts[len(parts)-1]
	var b strings.Builder
	for _, r := range last {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}
