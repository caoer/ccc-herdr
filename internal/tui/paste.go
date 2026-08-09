// Package tui holds helpers shared by the jump and openfile popups.
package tui

import "strings"

// SanitizePaste flattens pasted text into a single-line query fragment:
// newlines and tabs become spaces, other control characters are dropped.
func SanitizePaste(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case r < 0x20 || r == 0x7f:
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
