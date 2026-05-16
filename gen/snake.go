package gen

import (
	"strings"
	"unicode"
)

// SnakeCase converts CamelCase to snake_case.
//
//	UserStatus  → user_status
//	Lang        → lang
//	HTTPHandler → http_handler
func SnakeCase(s string) string {
	var result []rune
	runes := []rune(s)
	for i, r := range runes {
		if unicode.IsUpper(r) {
			// insert underscore before uppercase that follows a lowercase,
			// or before an uppercase that is followed by a lowercase (e.g. HTTPHandler)
			if i > 0 {
				prev := runes[i-1]
				if unicode.IsLower(prev) || unicode.IsDigit(prev) {
					result = append(result, '_')
				} else if unicode.IsUpper(prev) && i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
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

// ToFixed replaces hyphens with underscores for use in Go identifiers.
//
//	zh-CN → zh_CN
func ToFixed(s string) string {
	return strings.ReplaceAll(s, "-", "_")
}
